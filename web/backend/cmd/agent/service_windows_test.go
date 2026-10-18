//go:build windows

package main

import (
	"context"
	"golang.org/x/sys/windows/svc"
	"testing"
	"time"
)

func TestServiceStopCancelsAgentAndWaitsForItsCleanup(t *testing.T) {
	requests := make(chan svc.ChangeRequest, 1)
	statuses := make(chan svc.Status, 10)
	cleaned := make(chan struct{})
	finished := make(chan uint32, 1)
	handler := serviceHandler{run: func(ctx context.Context) error { <-ctx.Done(); close(cleaned); return ctx.Err() }}
	go func() { _, code := handler.Execute(nil, requests, statuses); finished <- code }()
	if state := (<-statuses).State; state != svc.StartPending {
		t.Fatal(state)
	}
	if state := (<-statuses).State; state != svc.Running {
		t.Fatal(state)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatal(code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("service did not stop")
	}
	select {
	case <-cleaned:
	default:
		t.Fatal("service exited without waiting for cleanup")
	}
}
