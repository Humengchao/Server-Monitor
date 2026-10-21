package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"server-monitor/internal/models"
)

func TestNeverSampledOfflineCountsFromCreation(t *testing.T) {
	now := time.Now()
	for _, elapsed := range []time.Duration{time.Minute, 29 * time.Minute, 30 * time.Minute, 24 * time.Hour} {
		snap := models.AlertSnapshot{CreatedAt: now.Add(-elapsed)}
		value, state := EvaluateRule(rule(models.AlertMetricOffline, ">", 0, 1800), snap, now)
		want := AlertClear
		if elapsed >= 30*time.Minute {
			want = AlertBreached
		}
		if value != elapsed.Seconds() || state != want {
			t.Fatalf("elapsed=%s age=%v state=%v", elapsed, value, state)
		}
		if _, state := EvaluateRule(rule(models.AlertMetricCPU, "<", 10, 30), snap, now); state != AlertClear {
			t.Fatal("missing metrics triggered threshold rule")
		}
	}
}

func TestSFTPCancellationAfterChannelAcceptance(t *testing.T) {
	for _, phase := range []string{"subsystem", "handshake", "directory"} {
		t.Run(phase, func(t *testing.T) {
			var silent atomic.Bool
			address, channels, _ := startSessionTestSSHServerWithSilence(t, &silent)
			client, err := newTestCache(t).Get(testCacheServer(t, address))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if phase == "directory" {
					store := NewSFTPDirectoryFiles(ctx, client, "linux").(ManagedFiles)
					_, err := store.Page("/", 0, 10)
					done <- err
				} else {
					_, err := NewSFTPFiles(ctx, client, "linux")
					done <- err
				}
			}()
			channel, requests, err := awaitSessionChannel(t, channels).Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer channel.Close()
			request := <-requests
			if request.Type != "subsystem" {
				t.Fatal(request.Type)
			}
			if phase != "subsystem" {
				_ = request.Reply(true, nil)
			}
			silent.Store(true)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel result=%v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation waited for peer response")
			}
		})
	}
}

func TestSFTPDirectoryOpensOneSessionAndResolvesSymlinks(t *testing.T) {
	handlers := sftp.InMemHandler()
	var sessions atomic.Int32
	server := aiSFTPServer(t, func(channel ssh.Channel) {
		sessions.Add(1)
		remote := sftp.NewRequestServer(channel, handlers)
		defer remote.Close()
		_ = remote.Serve()
	})
	client, _, err := DialServer(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// Build the fixture through its own session, then count only the page.
	remote, err := NewSFTPFiles(context.Background(), client, server.ServerType)
	if err != nil {
		t.Fatal(err)
	}
	store := remote.(*sftpFiles)
	if err := store.Write("/target.txt", strings.NewReader("hello"), 5, false); err != nil {
		t.Fatal(err)
	}
	if err := store.client.Symlink("/target.txt", "/linked.txt"); err != nil {
		t.Fatal(err)
	}
	_ = remote.Close()
	sessions.Store(0)
	page := NewSFTPDirectoryFiles(context.Background(), client, server.ServerType).(ManagedFiles)
	defer page.Close()
	listing, err := page.Page("/", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if sessions.Load() != 1 {
		t.Fatalf("page opened %d sessions", sessions.Load())
	}
	for _, entry := range listing.Entries {
		if entry.Name == "linked.txt" {
			if !entry.IsSymlink || !entry.IsFile || entry.Size != 5 {
				t.Fatalf("symlink metadata lost: %+v", entry)
			}
			return
		}
	}
	t.Fatal("symlink missing from directory page")
}

func TestSFTPSubsystemCancellationEventuallyClosesSilentTransport(t *testing.T) {
	var silent atomic.Bool
	address, channels, _ := startSessionTestSSHServerWithSilence(t, &silent)
	client, err := newTestCache(t).Get(testCacheServer(t, address))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := NewSFTPFiles(ctx, client, "linux"); result <- err }()
	channel, requests, err := awaitSessionChannel(t, channels).Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	<-requests
	silent.Store(true)
	cancel()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("subsystem cancellation blocked")
	}
	closed := make(chan struct{})
	go func() { _ = client.Wait(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("cancellation immediately closed shared transport")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-closed:
	case <-time.After(sshSessionCleanupGrace + time.Second):
		t.Fatal("silent transport leaked after cleanup grace")
	}
}

func respondReviewCommand(t *testing.T, channels <-chan ssh.NewChannel, expected, stdout, stderr string, exit uint32) {
	t.Helper()
	channel, requests, err := awaitSessionChannel(t, channels).Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer channel.Close()
	request := <-requests
	var payload struct{ Command string }
	if ssh.Unmarshal(request.Payload, &payload) != nil || payload.Command != expected {
		t.Fatalf("command=%q want=%q", payload.Command, expected)
	}
	_ = request.Reply(true, nil)
	fmt.Fprint(channel, stdout)
	fmt.Fprint(channel.Stderr(), stderr)
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{exit}))
}

func TestDockerLogsPreserveBothStreamsAndMutationIsNotRetried(t *testing.T) {
	address, channels, _ := startSessionTestSSHServer(t)
	client, err := newTestCache(t).Get(testCacheServer(t, address))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type response struct {
		output string
		err    error
	}
	done := make(chan response, 1)
	go func() {
		output, err := RunDockerContext(ctx, client, "logs --tail 1 abcdef012345", 1024)
		done <- response{output, err}
	}()
	respondReviewCommand(t, channels, "docker logs --tail 1 abcdef012345", "normal log\n", "error log\n", 0)
	result := <-done
	if result.err != nil || !strings.Contains(result.output, "normal log") || !strings.Contains(result.output, "error log") {
		t.Fatal(result)
	}
	go func() { done <- response{err: RunDockerMutationContext(ctx, client, "restart abcdef012345")} }()
	respondReviewCommand(t, channels, "docker info --format '{{.ServerVersion}}'", "27.0", "", 0)
	respondReviewCommand(t, channels, "docker restart abcdef012345", "", "permission denied connecting to docker.sock after restart", 1)
	select {
	case result := <-done:
		if result.err == nil {
			t.Fatal("failed action reported success")
		}
	case <-channels:
		t.Fatal("mutation retried with sudo")
	case <-time.After(time.Second):
		t.Fatal("mutation did not return")
	}
}

func TestAIFileExportRequiresSpecificApprovalAndFiltersRenamedRedisSecrets(t *testing.T) {
	for _, filename := range []string{"/etc/redis/redis.conf", "/etc/redis/users.acl"} {
		if _, err := PrepareAIAction(&models.Server{ServerType: "linux"}, AIToolCall{Function: AIFunctionCall{Name: "read_file", Arguments: stringMustJSON(aiReadFile{Path: filename})}}); err == nil {
			t.Fatal("sensitive service configuration allowed", filename)
		}
	}
	public := prepareAITest(t, "linux", "read_file", aiReadFile{Path: "/etc/os-release"})
	if _, err := aiActionApproval("readonly", public); err != nil {
		t.Fatal(err)
	}
	unknown := prepareAITest(t, "linux", "read_file", aiReadFile{Path: "/srv/app/settings.txt"})
	if approved, err := aiActionApproval("standard", unknown); err != nil || !approved {
		t.Fatal("arbitrary file exported without review", err)
	}
	if _, err := aiActionApproval("readonly", unknown); err == nil {
		t.Fatal("readonly mode authorizes arbitrary file export")
	}
	server := aiSFTPServer(t)
	filename := filepath.ToSlash(filepath.Join(t.TempDir(), "renamed-settings.txt"))
	for _, content := range []string{"requirepass fictional-password\n", "masterauth fictional-password\n", "user default on >fictional-password ~* +@all\n"} {
		if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		action := prepareAITest(t, runtime.GOOS, "read_file", aiReadFile{Path: filename})
		action.ApprovalMode = "standard" // Even an approved read must not export recognized credentials.
		result, err := ExecuteAIAction(context.Background(), server, action)
		if err == nil || result.Output != "" || !strings.Contains(err.Error(), "credential") {
			t.Fatalf("credential reached output: %+v %v", result, err)
		}
	}
}

func TestNotificationSecretsStayBoundToDestination(t *testing.T) {
	email := models.NotificationConfig{SMTPHost: "smtp.example", SMTPPort: 587, TLSMode: "starttls", Username: "user", Password: "fictional", From: "from@example.com", To: []string{"to@example.com"}}
	for _, change := range []map[string]json.RawMessage{{"smtp_host": json.RawMessage(`"other.example"`)}, {"smtp_port": json.RawMessage(`465`)}, {"username": json.RawMessage(`"other"`)}, {"tls_mode": json.RawMessage(`"tls"`)}} {
		if _, err := MergeNotificationConfig("email", email, change, nil); err == nil {
			t.Fatal("old SMTP password moved to new destination", change)
		}
	}
	old := models.NotificationConfig{WebhookURL: "https://one.example/hook", BearerToken: "fictional"}
	patch := map[string]json.RawMessage{"webhook_url": json.RawMessage(`"https://two.example/hook"`), "bearer_token": json.RawMessage(`""`)}
	if _, err := MergeNotificationConfig("webhook", old, patch, nil); err == nil {
		t.Fatal("blank secret authorized moving old bearer token")
	}
	if result, err := MergeNotificationConfig("webhook", old, patch, []string{"bearer_token"}); err != nil || result.BearerToken != "" {
		t.Fatal("explicit clear failed", err)
	}
	patch["bearer_token"] = json.RawMessage(`"new-fictional"`)
	if result, err := MergeNotificationConfig("webhook", old, patch, nil); err != nil || result.BearerToken != "new-fictional" {
		t.Fatal("explicit replacement failed", err)
	}
	patch["webhook_url"] = json.RawMessage(`"http://two.example/hook"`)
	if _, err := MergeNotificationConfig("webhook", old, patch, nil); err == nil {
		t.Fatal("bearer token accepted over plaintext HTTP")
	}
}

func TestTerminalRegistryRevokesSessionsAndRejectsLateRegistration(t *testing.T) {
	user := uuid.New()
	version := time.Now().Add(-time.Minute)
	var closed atomic.Int32
	unregister, err := RegisterTerminalSession(user, version, time.Now().Add(time.Hour), func() { closed.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	cutoff := time.Now()
	RevokeUserTerminalSessions(user, cutoff)
	if closed.Load() != 1 {
		t.Fatal("active terminal survived password change")
	}
	if _, err := RegisterTerminalSession(user, version, time.Now().Add(time.Hour), func() {}); err == nil {
		t.Fatal("old in-flight authentication registered after revocation")
	}
	expired := make(chan struct{})
	fresh, err := RegisterTerminalSession(user, cutoff, time.Now().Add(30*time.Millisecond), func() { close(expired) })
	if err != nil {
		t.Fatal(err)
	}
	defer fresh()
	select {
	case <-expired:
	case <-time.After(time.Second):
		t.Fatal("expired token kept terminal alive")
	}
}
