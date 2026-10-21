package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"server-monitor/internal/models"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

const (
	// BatchMaxTargets bounds one request and its worst-case time budget.
	BatchMaxTargets = 50
	// BatchConcurrency limits simultaneous SSH sessions so a wide batch cannot
	// exhaust the process's file descriptors or the hosts' MaxSessions.
	BatchConcurrency = 8
	// BatchCommandTimeout is per host, not for the batch as a whole.
	BatchCommandTimeout = 60 * time.Second
	// Seven waves of 50 targets, each with at most 10 seconds connecting and
	// 60 seconds executing, fit inside nine minutes. Proxy/client budgets are
	// longer so successful results reach the operator before those time out.
	BatchTimeout = 9 * time.Minute
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	output, truncated, err := runCmdContext(ctx, client, cmd, limit)
	if errors.Is(err, context.DeadlineExceeded) {
		// Preserve the existing API message while exposing a typed timeout to
		// callers deciding whether retrying the command is appropriate.
		err = &sshCommandTimeoutError{timeout: timeout}
	}
	return output, truncated, err
}

type sshCommandTimeoutError struct{ timeout time.Duration }

func (err *sshCommandTimeoutError) Error() string {
	return fmt.Sprintf("timed out after %s", err.timeout)
}

func (*sshCommandTimeoutError) Unwrap() error { return context.DeadlineExceeded }

func RunBatchCommandContext(ctx context.Context, cache *SSHConnCache, servers []*models.Server, cmd string) []BatchResult {
	return runBatch(ctx, servers, func(ctx context.Context, s *models.Server) BatchResult {
		started := time.Now()
		result := BatchResult{ServerID: s.ID, ServerName: s.Name}
		client, err := cache.GetContext(ctx, s)
		if err != nil {
			result.Error = "SSH connection failed: " + cleanCommandError(err.Error())
			result.DurationMS = time.Since(started).Milliseconds()
			return result
		}
		commandContext, cancel := context.WithTimeout(ctx, BatchCommandTimeout)
		defer cancel()
		result.Output, result.Truncated, err = runCmdContext(commandContext, client, cmd, BatchOutputLimit)
		result.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			result.Error = cleanCommandError(err.Error())
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				result.Error += "; remote completion is unknown; check the host before retrying"
			}
		} else {
			result.OK = true
		}
		return result
	})
}

func runBatch(ctx context.Context, servers []*models.Server, run func(context.Context, *models.Server) BatchResult) []BatchResult {
	results := make([]BatchResult, len(servers))
	slots := make(chan struct{}, BatchConcurrency)
	var wg sync.WaitGroup

	for i, server := range servers {
		wg.Add(1)
		go func(index int, s *models.Server) {
			defer wg.Done()
			results[index] = BatchResult{ServerID: s.ID, ServerName: s.Name, Error: "not started: request canceled"}
			select {
			case <-ctx.Done():
				return
			case slots <- struct{}{}:
			}
			defer func() { <-slots }()
			// If both cases were ready, cancellation still wins before executing.
			if ctx.Err() != nil {
				return
			}
			results[index] = run(ctx, s)
		}(i, server)
	}
	wg.Wait()
	return results
}
