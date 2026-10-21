package models

import (
	"context"
	"testing"
	"time"

	"server-monitor/internal/testdb"
)

func TestUptimeDetailQueriesCancelWhileWaitingForPostgresLocks(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	db.SetMaxOpenConns(2)
	until := time.Now().UTC()
	since := until.Add(-30 * 24 * time.Hour)
	for _, test := range []struct {
		name, table string
		query       func(context.Context) error
	}{
		{"ownership", "servers", func(ctx context.Context) error { _, err := GetServerCreatedAt(ctx, db, server, user); return err }},
		{"daily", "server_metrics_15m", func(ctx context.Context) error {
			_, err := GetDailyAvailability(ctx, db, server, since, until)
			return err
		}},
		{"gaps", "server_metrics_1m", func(ctx context.Context) error {
			_, err := GetMetricGaps(ctx, db, server, since, until, 120, 50)
			return err
		}},
		{"latest", "server_metrics_1m", func(ctx context.Context) error { _, err := GetLastMetricBucket(ctx, db, server, until); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			lock, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback()
			if _, err := lock.Exec("LOCK TABLE " + test.table + " IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- test.query(ctx) }()
			select {
			case err := <-done:
				if err == nil || ctx.Err() == nil {
					t.Fatalf("query did not honor its deadline while blocked: err=%v context=%v", err, ctx.Err())
				}
			case <-time.After(2 * time.Second):
				// Release the deliberately held lock even if context propagation
				// regresses, so the failed test cannot leave a stuck goroutine.
				lock.Rollback()
				<-done
				t.Fatal("query continued waiting after its deadline")
			}
			if err := lock.Rollback(); err != nil {
				t.Fatal(err)
			}
			check, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			if err := test.query(check); err != nil {
				t.Fatalf("connection not reusable after cancellation: %v", err)
			}
			if inUse := db.Stats().InUse; inUse != 0 {
				t.Fatalf("query left %d connections in use", inUse)
			}
		})
	}
}
