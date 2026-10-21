package services

import (
	"context"
	"testing"
	"time"

	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func TestIdleQueueWakeBypassesBackoffAndCancellationStopsIt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wake := make(chan struct{}, 1)
	calls := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runIdleQueue(ctx, wake, func(context.Context) bool { calls <- struct{}{}; return false })
	}()
	for i := 0; i < 3; i++ {
		wake <- struct{}{}
		select {
		case <-calls:
		case <-time.After(time.Second):
			t.Fatal("enqueue waited for idle timer")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("idle queue did not stop")
	}
	select {
	case <-calls:
		t.Fatal("unexpected delivery after cancellation")
	default:
	}
}

func TestNotificationEnqueueBroadcastsOnlyAfterCommit(t *testing.T) {
	db := testdb.Open(t)
	first, stopFirst := models.SubscribeNotificationWork(db)
	defer stopFirst()
	second, stopSecond := models.SubscribeNotificationWork(db)
	defer stopSecond()
	user, server, rule := testdb.Seed(t, db)
	if _, err := models.OpenAlertEventWithNotification(context.Background(), db, &models.AlertEvent{UserID: user, ServerID: server, RuleID: rule, StartedAt: time.Now()}, nil); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []<-chan struct{}{first, second} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("a dispatcher missed committed work")
		}
	}
	stopFirst()
}
