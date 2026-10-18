package services

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/crypto/ssh"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const LogsOutputLimit = 512 * 1024
const LogsTimeout = 12 * time.Second

var dockerLogsID = regexp.MustCompile(`^[a-fA-F0-9]{1,64}$`)

type LogsQuery struct {
	Since    time.Time
	Until    time.Time
	Unit     string
	Priority string
	Keyword  string
	Cursor   string
	Limit    int
}
type LogEntry struct {
	ID       string    `json:"id"`
	Time     time.Time `json:"time"`
	Message  string    `json:"message"`
	Unit     string    `json:"unit,omitempty"`
	Priority string    `json:"priority,omitempty"`
	Stream   string    `json:"stream"`
}
type LogsPage struct {
	Entries    []LogEntry `json:"entries"`
	NextCursor string     `json:"next_cursor,omitempty"`
	Truncated  bool       `json:"truncated"`
	Partial    bool       `json:"partial"`
	Reason     string     `json:"reason,omitempty"`
	Scanned    int        `json:"scanned"`
}
type logCursor struct {
	Kind     string `json:"k"`
	Query    string `json:"q"`
	Position string `json:"p"`
	Skip     int    `json:"s,omitempty"`
	Stdout   int    `json:"o,omitempty"`
	Stderr   int    `json:"e,omitempty"`
}
type LogsCommandResult struct {
	Stdout, Stderr string
	Truncated      bool
}
type LogsRunner func(context.Context, string) (LogsCommandResult, error)

func quoteLogArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
func logQueryKey(q LogsQuery) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s", q.Since.Format(time.RFC3339Nano), q.Until.Format(time.RFC3339Nano), q.Unit, q.Priority, q.Keyword)))
	return fmt.Sprintf("%x", sum[:12])
}
func decodeLogCursor(q LogsQuery, kind string) (logCursor, error) {
	c := logCursor{Kind: kind, Query: logQueryKey(q)}
	if q.Cursor == "" {
		return c, nil
	}
	if len(q.Cursor) > 8192 {
		return c, errors.New("cursor too long")
	}
	bytes, err := base64.RawURLEncoding.DecodeString(q.Cursor)
	if err != nil {
		return c, errors.New("invalid cursor")
	}
	if err = json.Unmarshal(bytes, &c); err != nil || c.Kind != kind || c.Query != logQueryKey(q) || len(c.Position) > 4096 || strings.ContainsAny(c.Position, "\x00\r\n") || c.Skip < 0 || c.Skip > 10000 || c.Stdout < 0 || c.Stdout > 10000 || c.Stderr < 0 || c.Stderr > 10000 {
		return c, errors.New("cursor does not match query")
	}
	return c, nil
}
func encodeLogCursor(c logCursor) string {
	bytes, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(bytes)
}

// RunLogCommand keeps stderr separate: journal diagnostics are not JSON records;
// Docker's stderr is also a legitimate application log stream.
func RunLogCommand(ctx context.Context, client *ssh.Client, command string) (LogsCommandResult, error) {
	session, err := newSSHSession(ctx, client)
	if err != nil {
		return LogsCommandResult{}, err
	}
	stdout := &syncLimitedWriter{limit: LogsOutputLimit}
	stderr := &syncLimitedWriter{limit: LogsOutputLimit}
	session.Stdout, session.Stderr = stdout, stderr
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		e := ctx.Err()
		if e == nil {
			e = session.Run(command)
		}
		_ = session.Close()
		done <- e
	}()
	read := func() LogsCommandResult {
		out, ot := stdout.result()
		e, et := stderr.result()
		return LogsCommandResult{out, e, ot || et}
	}
	select {
	case err := <-done:
		return read(), err
	case <-ctx.Done():
		cleanupSSHOperation(client, func() { _ = session.Close(); <-finished })
		return read(), ctx.Err()
	}
}

func JournalCommand(q LogsQuery) (string, logCursor, error) {
	cursor, err := decodeLogCursor(q, "journal")
	if err != nil {
		return "", cursor, err
	}
	command := fmt.Sprintf("LC_ALL=C SYSTEMD_COLORS=0 journalctl --no-pager --output=json --lines=+%d", q.Limit+1)
	// journalctl --since and --after-cursor are mutually exclusive.
	if cursor.Position != "" {
		command += " --after-cursor=" + quoteLogArgument(cursor.Position)
	} else {
		command += " --since=" + quoteLogArgument(q.Since.UTC().Format("2006-01-02 15:04:05.000000 UTC"))
	}
	if !q.Until.IsZero() {
		command += " --until=" + quoteLogArgument(q.Until.UTC().Format("2006-01-02 15:04:05.000000 UTC"))
	}
	if q.Unit != "" {
		if !ValidServiceName(q.Unit) {
			return "", cursor, errors.New("invalid service")
		}
		command += " --unit=" + quoteLogArgument(q.Unit)
	}
	if q.Priority != "" {
		level, err := strconv.Atoi(q.Priority)
		if err != nil || level < 0 || level > 7 {
			return "", cursor, errors.New("invalid priority")
		}
		command += " --priority=" + strconv.Itoa(level)
	}
	return command, cursor, nil
}
func logField(data map[string]json.RawMessage, name string) string {
	var value string
	if json.Unmarshal(data[name], &value) == nil {
		return value
	}
	var bytes []byte
	if json.Unmarshal(data[name], &bytes) == nil {
		return string(bytes)
	}
	var values []string
	if json.Unmarshal(data[name], &values) == nil {
		return strings.Join(values, "\n")
	}
	return ""
}
func ReadJournal(ctx context.Context, q LogsQuery, run LogsRunner) (LogsPage, error) {
	page := LogsPage{Entries: []LogEntry{}}
	command, cursor, err := JournalCommand(q)
	if err != nil {
		return page, err
	}
	output, runErr := run(ctx, command)
	page.Truncated = output.Truncated
	scanner := bufio.NewScanner(strings.NewReader(output.Stdout))
	scanner.Buffer(make([]byte, 4096), LogsOutputLimit)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if page.Scanned >= q.Limit {
			page.Truncated = true
			break
		}
		var data map[string]json.RawMessage
		if json.Unmarshal(line, &data) != nil {
			page.Partial = true
			page.Reason = "invalid_record"
			continue
		}
		position := logField(data, "__CURSOR")
		micro, parseErr := strconv.ParseInt(logField(data, "__REALTIME_TIMESTAMP"), 10, 64)
		if position == "" || parseErr != nil {
			page.Partial = true
			page.Reason = "invalid_record"
			continue
		}
		page.Scanned++
		cursor.Position = position
		entry := LogEntry{ID: position, Time: time.UnixMicro(micro).UTC(), Message: logField(data, "MESSAGE"), Unit: logField(data, "_SYSTEMD_UNIT"), Priority: logField(data, "PRIORITY"), Stream: "journal"}
		if q.Keyword == "" || strings.Contains(entry.Message, q.Keyword) {
			page.Entries = append(page.Entries, entry)
		}
	}
	if scanner.Err() != nil {
		page.Truncated = true
		page.Partial = true
		page.Reason = "byte_limit"
	}
	if cursor.Position != "" {
		page.NextCursor = encodeLogCursor(cursor)
	}
	diagnostics := strings.ToLower(output.Stderr)
	if strings.Contains(diagnostics, "permission denied") || strings.Contains(diagnostics, "access denied") || strings.Contains(diagnostics, "not seeing messages") {
		page.Partial = true
		page.Reason = "access_denied"
	}
	if runErr != nil {
		page.Partial = true
		if page.Reason == "" {
			page.Reason = "command_failed"
		}
		if errors.Is(runErr, context.DeadlineExceeded) {
			page.Reason = "timeout"
		}
		if strings.Contains(diagnostics, "not found") {
			page.Reason = "unsupported"
		}
		if strings.Contains(diagnostics, "cursor") {
			page.Reason = "cursor_unavailable"
		}
	}
	if output.Truncated {
		page.Partial = true
		page.Reason = "byte_limit"
	}
	return page, nil
}

func DockerLogsCommand(q LogsQuery, container string) (string, logCursor, error) {
	if !dockerLogsID.MatchString(container) {
		return "", logCursor{}, errors.New("invalid container id")
	}
	cursor, err := decodeLogCursor(q, "docker:"+container)
	if err != nil {
		return "", cursor, err
	}
	since := q.Since
	if cursor.Position != "" {
		since, err = time.Parse(time.RFC3339Nano, cursor.Position)
		if err != nil {
			return "", cursor, errors.New("invalid cursor timestamp")
		}
	}
	// All arguments here are generated timestamps, bounded counts, and a hex ID;
	// bare safe tokens work with both POSIX shells and Windows OpenSSH's cmd.exe.
	command := fmt.Sprintf("docker logs --timestamps --tail %d --since %s", q.Limit+1, since.UTC().Format(time.RFC3339Nano))
	if !q.Until.IsZero() {
		command += " --until " + q.Until.UTC().Format(time.RFC3339Nano)
	}
	return command + " " + container, cursor, nil
}
func ReadDockerLogs(ctx context.Context, q LogsQuery, container string, run LogsRunner) (LogsPage, error) {
	page := LogsPage{Entries: []LogEntry{}}
	command, cursor, err := DockerLogsCommand(q, container)
	if err != nil {
		return page, err
	}
	output, runErr := run(ctx, command)
	// Non-interactive sudo only after a genuine CLI access failure, never because
	// application stderr contained a scary-looking line.
	if runErr != nil && output.Stdout == "" && ctx.Err() == nil && (strings.Contains(output.Stderr, "permission denied") || strings.Contains(output.Stderr, "permission to connect")) {
		output, runErr = run(ctx, "sudo -n "+command)
	}
	records := []LogEntry{}
	for _, stream := range []struct{ name, text string }{{"stdout", output.Stdout}, {"stderr", output.Stderr}} {
		scanner := bufio.NewScanner(strings.NewReader(stream.text))
		scanner.Buffer(make([]byte, 4096), LogsOutputLimit)
		for scanner.Scan() {
			line := scanner.Text()
			at, message, found := strings.Cut(line, " ")
			stamp, err := time.Parse(time.RFC3339Nano, at)
			if !found || err != nil {
				if line != "" {
					page.Partial = true
					page.Reason = "unparsed_output"
				}
				continue
			}
			records = append(records, LogEntry{Time: stamp, Message: message, Stream: stream.name})
		}
		if scanner.Err() != nil {
			page.Partial = true
			page.Truncated = true
			page.Reason = "byte_limit"
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].Time.Equal(records[j].Time) {
			return records[i].Stream < records[j].Stream
		}
		return records[i].Time.Before(records[j].Time)
	})
	if len(records) > q.Limit {
		page.Truncated = true
		page.Partial = true
		page.Reason = "tail_limit"
		records = records[len(records)-q.Limit:]
	}
	skips := map[string]int{"stdout": cursor.Stdout, "stderr": cursor.Stderr}
	counts := map[string]int{"stdout": 0, "stderr": 0}
	lastPosition := ""
	for _, entry := range records {
		position := entry.Time.UTC().Format(time.RFC3339Nano)
		if position != lastPosition {
			counts = map[string]int{"stdout": 0, "stderr": 0}
			lastPosition = position
		}
		counts[entry.Stream]++
		if position == cursor.Position && skips[entry.Stream] > 0 {
			skips[entry.Stream]--
			continue
		}
		page.Scanned++
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s", position, counts[entry.Stream], entry.Stream, entry.Message)))
		entry.ID = fmt.Sprintf("%x", sum[:16])
		if q.Keyword == "" || strings.Contains(entry.Message, q.Keyword) {
			page.Entries = append(page.Entries, entry)
		}
	}
	if lastPosition != "" {
		cursor.Position = lastPosition
		cursor.Stdout = counts["stdout"]
		cursor.Stderr = counts["stderr"]
		page.NextCursor = encodeLogCursor(cursor)
	} else if cursor.Position != "" {
		page.NextCursor = encodeLogCursor(cursor)
	}
	if runErr != nil {
		page.Partial = true
		page.Reason = "command_failed"
		if errors.Is(runErr, context.DeadlineExceeded) {
			page.Reason = "timeout"
		}
		if strings.Contains(strings.ToLower(output.Stderr), "permission denied") {
			page.Reason = "access_denied"
		}
	}
	if output.Truncated {
		page.Partial = true
		page.Truncated = true
		page.Reason = "byte_limit"
	}
	return page, nil
}
