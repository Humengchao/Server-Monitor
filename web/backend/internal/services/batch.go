package services

import (
	"bytes"
	"fmt"
	"sync"
	"time"

	"server-monitor/internal/models"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const (
	// BatchMaxTargets bounds one request. Fifty hosts at the concurrency below
	// is already a few seconds of wall clock; more belongs in a real
	// configuration-management tool.
	BatchMaxTargets = 50
	// BatchConcurrency limits simultaneous SSH sessions so a wide batch cannot
	// exhaust the process's file descriptors or the hosts' MaxSessions.
	BatchConcurrency = 8
	// BatchCommandTimeout is per host, not for the batch as a whole.
	BatchCommandTimeout = 60 * time.Second
	// BatchOutputLimit caps each host's captured output. A runaway command
	// (`yes`, `cat /dev/urandom`) must not stream gigabytes into memory.
	BatchOutputLimit = 64 * 1024
	// BatchMaxCommandLength keeps a pasted script from becoming the payload.
	BatchMaxCommandLength = 4096
)

// BatchResult is one host's outcome. Output holds combined stdout+stderr,
// truncated at BatchOutputLimit.
type BatchResult struct {
	ServerID   uuid.UUID `json:"server_id"`
	ServerName string    `json:"server_name"`
	OK         bool      `json:"ok"`
	Output     string    `json:"output"`
	Error      string    `json:"error"`
	Truncated  bool      `json:"truncated"`
	DurationMS int64     `json:"duration_ms"`
}

// syncLimitedWriter collects combined output. x/crypto/ssh copies stdout and
// stderr on separate goroutines, so the writer must be safe for concurrent use;
// the cap keeps a chatty command from ballooning memory while still letting the
// command run to completion.
type syncLimitedWriter struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (w *syncLimitedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if remaining := w.limit - w.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			w.buf.Write(p[:remaining])
			w.truncated = true
		} else {
			w.buf.Write(p)
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	// Always report a full write: returning short would make io.Copy abort with
	// ErrShortWrite and surface as a command failure, when in fact the command
	// was simply louder than we care to record.
	return len(p), nil
}

func (w *syncLimitedWriter) result() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String(), w.truncated
}

// RunCmdBounded runs a command capturing combined output, giving up after
// timeout and never buffering more than limit bytes.
func RunCmdBounded(client *ssh.Client, cmd string, timeout time.Duration, limit int) (string, bool, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", false, err
	}
	writer := &syncLimitedWriter{limit: limit}
	sess.Stdout = writer
	sess.Stderr = writer

	// Buffered so the goroutine can always deliver its result and exit, even
	// when nobody is left reading after a timeout.
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case runErr := <-done:
		sess.Close()
		output, truncated := writer.result()
		return output, truncated, runErr
	case <-timer.C:
		// Close our side and return immediately. Waiting for Run to unwind here
		// would defeat the timeout: closing the channel does not make the
		// remote command exit, so a `sleep 90` under a 60s limit would still
		// hold this concurrency slot for the full 90 seconds.
		//
		// The abandoned goroutine finishes whenever the remote command does and
		// then exits; reading the writer is safe because it is mutex-guarded
		// and result() takes a snapshot.
		sess.Close()
		output, truncated := writer.result()
		return output, truncated, fmt.Errorf("timed out after %s", timeout)
	}
}

// RunBatchCommand executes cmd on every server concurrently and returns one
// result per server, in the same order as the input. Individual failures never
// abort the batch: a host that is unreachable simply reports its error.
func RunBatchCommand(cache *SSHConnCache, servers []*models.Server, cmd string) []BatchResult {
	results := make([]BatchResult, len(servers))
	slots := make(chan struct{}, BatchConcurrency)
	var wg sync.WaitGroup

	for i, server := range servers {
		wg.Add(1)
		go func(index int, s *models.Server) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			started := time.Now()
			result := BatchResult{ServerID: s.ID, ServerName: s.Name}

			client, err := cache.Get(s)
			if err != nil {
				result.Error = "SSH connection failed: " + cleanCommandError(err.Error())
				result.DurationMS = time.Since(started).Milliseconds()
				results[index] = result
				return
			}
			output, truncated, runErr := RunCmdBounded(client, cmd, BatchCommandTimeout, BatchOutputLimit)
			result.Output = output
			result.Truncated = truncated
			result.DurationMS = time.Since(started).Milliseconds()
			if runErr != nil {
				result.Error = runErr.Error()
			} else {
				result.OK = true
			}
			results[index] = result
		}(i, server)
	}
	wg.Wait()
	return results
}
