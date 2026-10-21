package services

import (
	"context"
	"time"
)

// Keep a short bounded recovery scan for durable queues, and wake immediately
// for local enqueues. Active batches retain the normal one-second cadence.
func runIdleQueue(ctx context.Context, wake <-chan struct{}, work func(context.Context) bool) {
	delay := time.Second
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			delay = time.Second
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		if work(ctx) {
			delay = time.Second
		} else {
			delay = min(delay*2, 5*time.Second)
		}
		timer.Reset(delay)
	}
}
