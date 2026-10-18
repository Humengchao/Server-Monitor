package services

import (
	"context"
	"github.com/google/uuid"
	"server-monitor/internal/models"
	"sync/atomic"
	"testing"
	"time"
)

func TestBatchCancellationDoesNotStartQueuedHosts(t *testing.T) {
	servers := make([]*models.Server, 50)
	for i := range servers {
		servers[i] = &models.Server{ID: uuid.New(), Name: "host"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	entered := make(chan struct{}, BatchConcurrency)
	done := make(chan []BatchResult, 1)
	go func() {
		done <- runBatch(ctx, servers, func(ctx context.Context, s *models.Server) BatchResult {
			calls.Add(1)
			entered <- struct{}{}
			<-ctx.Done()
			return BatchResult{ServerID: s.ID, Error: "canceled"}
		})
	}()
	for i := 0; i < BatchConcurrency; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("worker not started")
		}
	}
	cancel()
	select {
	case results := <-done:
		if len(results) != 50 || calls.Load() != BatchConcurrency {
			t.Fatalf("canceled queue executed: calls=%d results=%d", calls.Load(), len(results))
		}
		for i, result := range results {
			if result.ServerID != servers[i].ID || result.Error == "" {
				t.Fatalf("missing target outcome: %+v", result)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("canceled batch did not return")
	}
}

func TestSSHCacheCanceledWaiterDoesNotDial(t *testing.T) {
	cache := newTestCache(t)
	server := &models.Server{ID: uuid.New()}
	entry := &sshCacheEntry{}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	cache.entries[server.ID] = entry
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := cache.GetContext(ctx, server); err != context.DeadlineExceeded {
		t.Fatalf("waiting for entry returned %v", err)
	}
}

func TestSSHHandshakeCanBeCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := dialSSHClientConnContext(ctx, "192.0.2.1", 22, "test", "password", "", "", time.Minute)
	if err == nil {
		t.Fatal("canceled dial succeeded")
	}
}
