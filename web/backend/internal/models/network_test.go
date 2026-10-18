package models

import (
	"context"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"server-monitor/internal/testdb"
	"testing"
	"time"
)

func TestNetworkOwnershipWeightedHistoryAndPairChanges(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	other, foreign, _ := testdb.Seed(t, db)
	ctx := context.Background()
	check := NetworkCheck{ID: uuid.New(), UserID: user, Name: "local to host", TargetServerID: server, Packets: 5, Enabled: true, LossThreshold: 10}
	if err := SaveNetworkCheck(ctx, db, &check, true); err != nil {
		t.Fatal(err)
	}
	bad := check
	bad.ID = uuid.New()
	bad.TargetServerID = foreign
	if err := SaveNetworkCheck(ctx, db, &bad, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign target accepted: %v", err)
	}
	bad.TargetServerID = server
	bad.SourceServerID = &foreign
	if err := SaveNetworkCheck(ctx, db, &bad, true); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign source accepted: %v", err)
	}
	if _, err := GetNetworkCheck(ctx, db, check.ID, other); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign read: %v", err)
	}
	now := time.Now().UTC()
	five, one, zero := 5, 1, 0
	avg := 10.0
	for index, sample := range []NetworkSample{{Status: "ok", Sent: &five, Received: &five, AvgMS: &avg}, {Status: "down", Sent: &one, Received: &zero}, {Status: "error", ErrorCode: "source_unreachable"}} {
		sample.RecordedAt = now.Add(time.Duration(index) * time.Second)
		if err := InsertNetworkSample(ctx, db, check, sample); err != nil {
			t.Fatal(err)
		}
	}
	samples, summary, err := NetworkHistory(ctx, db, check.ID, user, now.Add(-time.Second), now.Add(time.Minute), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || summary.Sent != 6 || summary.Received != 5 || summary.KnownSamples != 2 || summary.UnknownSamples != 1 || summary.LossPercent == nil || *summary.LossPercent < 16.66 || *summary.LossPercent > 16.67 || summary.AvgMS == nil || *summary.AvgMS != 10 {
		t.Fatalf("%+v %+v", samples, summary)
	}
	// Source edits drop incomparable history; an old in-flight probe cannot insert afterwards.
	old := check
	check.SourceServerID = &server
	if err := SaveNetworkCheck(ctx, db, &check, false); err != nil {
		t.Fatal(err)
	}
	if err := InsertNetworkSample(ctx, db, old, NetworkSample{RecordedAt: now, Status: "ok"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale pair inserted: %v", err)
	}
	_, summary, err = NetworkHistory(ctx, db, check.ID, user, now.Add(-time.Second), now.Add(time.Minute), 100)
	if err != nil || summary.KnownSamples+summary.UnknownSamples != 0 {
		t.Fatalf("history was not reset: %+v %v", summary, err)
	}
	if err := DeleteNetworkCheck(ctx, db, check.ID, other); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign delete: %v", err)
	}
}
