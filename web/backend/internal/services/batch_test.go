package services

import (
	"strings"
	"sync"
	"testing"
)

func TestSyncLimitedWriterCaps(t *testing.T) {
	w := &syncLimitedWriter{limit: 10}

	n, err := w.Write([]byte("12345"))
	if err != nil || n != 5 {
		t.Fatalf("Write = (%d, %v), want (5, nil)", n, err)
	}
	out, truncated := w.result()
	if out != "12345" || truncated {
		t.Fatalf("after first write: out=%q truncated=%v", out, truncated)
	}

	// Straddles the limit: the prefix is kept and the overflow flagged.
	if n, err = w.Write([]byte("67890EXTRA")); err != nil || n != 10 {
		t.Fatalf("Write = (%d, %v); a short count would abort io.Copy with ErrShortWrite", n, err)
	}
	out, truncated = w.result()
	if out != "1234567890" {
		t.Fatalf("out = %q, want the first 10 bytes", out)
	}
	if !truncated {
		t.Fatal("truncated should be set once output exceeds the limit")
	}

	// Writes past a full buffer are still accepted, just discarded.
	if n, err = w.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("Write past limit = (%d, %v), want (4, nil)", n, err)
	}
	if out, _ = w.result(); out != "1234567890" {
		t.Fatalf("out = %q, want it unchanged", out)
	}
}

func TestSyncLimitedWriterConcurrent(t *testing.T) {
	// ssh copies stdout and stderr on separate goroutines, so the writer has to
	// tolerate concurrent use. Run under -race to make this meaningful.
	w := &syncLimitedWriter{limit: 1 << 20}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				w.Write([]byte("chunk\n"))
			}
		}()
	}
	wg.Wait()
	out, truncated := w.result()
	if truncated {
		t.Fatal("output should fit well under a 1 MiB limit")
	}
	if got := strings.Count(out, "chunk\n"); got != 1600 {
		t.Fatalf("wrote %d chunks, want 1600 — output was interleaved or lost", got)
	}
}

func TestSyncLimitedWriterZeroLimit(t *testing.T) {
	w := &syncLimitedWriter{limit: 0}
	if n, err := w.Write([]byte("anything")); err != nil || n != 8 {
		t.Fatalf("Write = (%d, %v), want (8, nil)", n, err)
	}
	out, truncated := w.result()
	if out != "" || !truncated {
		t.Fatalf("out=%q truncated=%v, want empty and truncated", out, truncated)
	}
}
