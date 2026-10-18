package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"io"
	"strings"
	"testing"
	"time"
)

func logTestQuery() LogsQuery {
	return LogsQuery{Since: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2030, 1, 1, 1, 0, 0, 0, time.UTC), Limit: 2}
}
func journalTestLine(id, message string) string {
	body, _ := json.Marshal(map[string]string{"__CURSOR": id, "__REALTIME_TIMESTAMP": "1893456000000000", "MESSAGE": message, "_SYSTEMD_UNIT": "app.service", "PRIORITY": "3"})
	return string(body) + "\n"
}
func TestJournalCursorLiteralFilteringAndBounds(t *testing.T) {
	q := logTestQuery()
	q.Keyword = ".*"
	q.Unit = "app.service"
	q.Priority = "3"
	run := func(ctx context.Context, command string) (LogsCommandResult, error) {
		if strings.Contains(command, "grep") || !strings.Contains(command, "--since=") || !strings.Contains(command, "--lines=+3") {
			t.Fatal(command)
		}
		return LogsCommandResult{Stdout: journalTestLine("c1", "not matching") + journalTestLine("c2", "literal .* present") + journalTestLine("c3", "third")}, nil
	}
	page, err := ReadJournal(context.Background(), q, run)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Truncated || page.Scanned != 2 || len(page.Entries) != 1 || page.Entries[0].ID != "c2" {
		t.Fatalf("%+v", page)
	}
	q.Cursor = page.NextCursor
	command, _, err := JournalCommand(q)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(command, "--since=") || !strings.Contains(command, "--after-cursor='c2'") {
		t.Fatal(command)
	}
	q.Keyword = "changed"
	if _, _, err = JournalCommand(q); err == nil {
		t.Fatal("cursor reused across filters")
	}
}
func TestJournalPermissionTimeoutMalformedAndBinaryMessage(t *testing.T) {
	q := logTestQuery()
	cases := []struct {
		output LogsCommandResult
		err    error
		reason string
	}{
		{LogsCommandResult{Stderr: "Hint: You are currently not seeing messages from other users"}, nil, "access_denied"},
		{LogsCommandResult{Stderr: "Permission denied"}, errors.New("exit 1"), "access_denied"},
		{LogsCommandResult{}, context.DeadlineExceeded, "timeout"},
		{LogsCommandResult{Stdout: "invalid\n"}, nil, "invalid_record"},
		{LogsCommandResult{Truncated: true}, nil, "byte_limit"},
	}
	for _, c := range cases {
		p, e := ReadJournal(context.Background(), q, func(context.Context, string) (LogsCommandResult, error) { return c.output, c.err })
		if e != nil || !p.Partial || p.Reason != c.reason {
			t.Fatalf("%+v %v", p, e)
		}
	}
	line := `{"__CURSOR":"binary","__REALTIME_TIMESTAMP":"1893456000000000","MESSAGE":[104,105]}`
	p, e := ReadJournal(context.Background(), q, func(context.Context, string) (LogsCommandResult, error) { return LogsCommandResult{Stdout: line}, nil })
	if e != nil || len(p.Entries) != 1 || p.Entries[0].Message != "hi" {
		t.Fatalf("%+v %v", p, e)
	}
}
func TestDockerLogsPreserveBothStreamsAndCursorBoundary(t *testing.T) {
	q := logTestQuery()
	q.Limit = 10
	stamp := "2030-01-01T00:01:00.000000000Z"
	run := func(context.Context, string) (LogsCommandResult, error) {
		return LogsCommandResult{Stdout: stamp + " stdout entry\n", Stderr: stamp + " stderr entry\n"}, nil
	}
	first, err := ReadDockerLogs(context.Background(), q, "abc123", run)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 2 || first.Entries[0].Stream != "stderr" || first.Entries[1].Stream != "stdout" || first.Partial {
		t.Fatalf("%+v", first)
	}
	q.Cursor = first.NextCursor
	second, err := ReadDockerLogs(context.Background(), q, "abc123", run)
	if err != nil || len(second.Entries) != 0 {
		t.Fatalf("repeated cursor duplicate: %+v %v", second, err)
	}
	added, err := ReadDockerLogs(context.Background(), q, "abc123", func(context.Context, string) (LogsCommandResult, error) {
		return LogsCommandResult{Stdout: stamp + " stdout entry\n", Stderr: stamp + " stderr entry\n" + stamp + " newly appended stderr\n"}, nil
	})
	if err != nil || len(added.Entries) != 1 || added.Entries[0].Message != "newly appended stderr" {
		t.Fatalf("same-timestamp stderr skipped: %+v %v", added, err)
	}
	q.Cursor = ""
	q.Limit = 1
	tail, err := ReadDockerLogs(context.Background(), q, "abc123", run)
	if err != nil || !tail.Truncated || tail.Reason != "tail_limit" {
		t.Fatalf("%+v %v", tail, err)
	}
}
func TestLogSSHTransportSeparatesStreamsAndCancellation(t *testing.T) {
	address, channels, _ := startSessionTestSSHServer(t)
	client, err := ssh.Dial("tcp", address, &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey()})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	complete := make(chan error, 1)
	go func() {
		channel, requests, err := (<-channels).Accept()
		if err != nil {
			complete <- err
			return
		}
		defer channel.Close()
		req := <-requests
		_ = req.Reply(true, nil)
		_, _ = io.WriteString(channel, "out\n")
		_, _ = io.WriteString(channel.Stderr(), "err\n")
		_, err = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		complete <- err
	}()
	output, err := RunLogCommand(context.Background(), client, "fixture")
	if err != nil || output.Stdout != "out\n" || output.Stderr != "err\n" {
		t.Fatalf("%+v %v", output, err)
	}
	if err = <-complete; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := RunLogCommand(ctx, client, "hold"); done <- err }()
	incoming := awaitSessionChannel(t, channels)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not return")
	}
	assertLateSessionClosed(t, incoming)
}
func TestLogArgumentsRejectInjectionAndCursorMismatch(t *testing.T) {
	q := logTestQuery()
	q.Unit = "app;reboot"
	if _, _, err := JournalCommand(q); err == nil {
		t.Fatal("unsafe unit")
	}
	q.Unit = ""
	q.Priority = "3;id"
	if _, _, err := JournalCommand(q); err == nil {
		t.Fatal("unsafe priority")
	}
	q.Priority = ""
	q.Cursor = "bad!"
	if _, _, err := JournalCommand(q); err == nil {
		t.Fatal("invalid cursor")
	}
	if quoted := quoteLogArgument("x'$(id)"); quoted != "'x'\"'\"'$(id)'" {
		t.Fatal(fmt.Sprintf("quote: %s", quoted))
	}
}
