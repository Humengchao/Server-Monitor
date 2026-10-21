package models

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"server-monitor/internal/testdb"

	"github.com/google/uuid"
)

func TestNetworkTrendFullWeekPreservesFaultsAndWeightedLatency(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	other, _, _ := testdb.Seed(t, db)
	ctx := context.Background()
	check := NetworkCheck{ID: uuid.New(), UserID: user, Name: "week", TargetServerID: server, Packets: 5, Enabled: true, LossThreshold: 10}
	if err := SaveNetworkCheck(ctx, db, &check, true); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
	until := since.Add(7 * 24 * time.Hour)
	_, err := db.Exec(`INSERT INTO network_samples(check_id,recorded_at,sample)
	 SELECT $1,$2::timestamptz+i*interval '1 minute',jsonb_build_object(
	 'recorded_at',$2::timestamptz+i*interval '1 minute','status',CASE i WHEN 0 THEN 'down' WHEN 1 THEN 'error' WHEN 2 THEN 'loss' ELSE 'ok' END,
	 'sent',CASE WHEN i=1 THEN NULL ELSE 5 END,'received',CASE i WHEN 0 THEN 0 WHEN 1 THEN NULL WHEN 2 THEN 1 ELSE 5 END,
	 'avg_ms',CASE WHEN i<2 THEN NULL WHEN i=2 THEN 100 ELSE 10 END,
	 'min_ms',CASE WHEN i<2 THEN NULL WHEN i=2 THEN 90 ELSE 8 END,
	 'max_ms',CASE WHEN i<2 THEN NULL WHEN i=2 THEN 200 ELSE 15 END,
	 'threshold_exceeded',i<3) FROM generate_series(0,10080) i`, check.ID, since)
	if err != nil {
		t.Fatal(err)
	}
	trend, err := NetworkTrendHistory(ctx, db, check.ID, user, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(trend.Buckets) > 500 || len(trend.Buckets) != 500 || !trend.Buckets[0].RecordedAt.Equal(since) || !trend.Buckets[len(trend.Buckets)-1].Until.Equal(until) {
		t.Fatalf("range not covered: %+v", trend)
	}
	s := trend.Summary
	wantAvg := float64(10078*5*10+100) / float64(10078*5+1)
	if s.SampleCount != 10081 || s.KnownSamples != 10080 || s.UnknownSamples != 1 || s.DownSamples != 1 || s.LossSamples != 1 || s.ThresholdExceededSamples != 2 || s.Sent != 50400 || s.Received != 50391 || s.AvgMS == nil || math.Abs(*s.AvgMS-wantAvg) > 1e-9 || s.MaxMS == nil || *s.MaxMS != 200 || s.MinMS == nil || *s.MinMS != 8 {
		t.Fatalf("bad full-window summary: %+v", s)
	}
	first := trend.Buckets[0]
	if first.Status != "down" || first.DownSamples != 1 || first.UnknownSamples != 1 || first.LossSamples != 1 || first.MaxMS == nil || *first.MaxMS != 200 {
		t.Fatalf("outage erased by average: %+v", first)
	}
	if trend.Buckets[len(trend.Buckets)-1].SampleCount == 0 {
		t.Fatal("inclusive final sample lost")
	}
	foreign, err := NetworkTrendHistory(ctx, db, check.ID, other, since, until)
	if err != nil || foreign.Summary.SampleCount != 0 {
		t.Fatalf("foreign history exposed: %+v %v", foreign.Summary, err)
	}
	count, pages := 0, 0
	cursor := ""
	seen := map[time.Time]bool{}
	for {
		samples, next, err := NetworkHistoryPage(ctx, db, check.ID, user, since, until, 2000, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, sample := range samples {
			if seen[sample.RecordedAt] {
				t.Fatal("duplicate page boundary")
			}
			seen[sample.RecordedAt] = true
		}
		count += len(samples)
		pages++
		if next == nil {
			break
		}
		cursor = *next
	}
	if count != 10081 || pages != 6 {
		t.Fatalf("pagination lost old history: %d samples, %d pages", count, pages)
	}
}

func TestNetworkHistoryCursorTiesScopeAndEmptyBuckets(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	ctx := context.Background()
	check := NetworkCheck{ID: uuid.New(), UserID: user, Name: "ties", TargetServerID: server, Packets: 5, Enabled: true, LossThreshold: 10}
	if err := SaveNetworkCheck(ctx, db, &check, true); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	for i := 0; i < 7; i++ {
		if err := InsertNetworkSample(ctx, db, check, NetworkSample{RecordedAt: since.Add(time.Minute), Status: "error", ErrorCode: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	trend, err := NetworkTrendHistory(ctx, db, check.ID, user, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if trend.BucketSeconds != 60 || len(trend.Buckets) != 60 {
		t.Fatalf("hourly trend must match minute collection cadence: %d seconds, %d buckets", trend.BucketSeconds, len(trend.Buckets))
	}
	if trend.Buckets[0].Status != "none" || trend.Buckets[0].AvgMS != nil || trend.Buckets[0].LossPercent != nil || trend.Buckets[0].SampleCount != 0 || trend.Summary.UnknownSamples != 7 || trend.Summary.AvgMS != nil || trend.Summary.LossPercent != nil {
		t.Fatalf("empty/unknown bucket fabricated measurements: %+v", trend.Summary)
	}
	seen := map[string]bool{}
	cursor := ""
	var firstCursor string
	for {
		samples, next, err := NetworkHistoryPage(ctx, db, check.ID, user, since, until, 2, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range samples {
			if seen[s.ErrorCode] {
				t.Fatal("duplicate timestamp row repeated")
			}
			seen[s.ErrorCode] = true
		}
		if next == nil {
			break
		}
		cursor = *next
		if firstCursor == "" {
			firstCursor = cursor
		}
	}
	if len(seen) != 7 {
		t.Fatalf("same-timestamp samples lost: %+v", seen)
	}
	for _, change := range []struct {
		id, user     uuid.UUID
		since, until time.Time
		cursor       string
	}{
		{uuid.New(), user, since, until, firstCursor}, {check.ID, uuid.New(), since, until, firstCursor},
		{check.ID, user, since.Add(time.Second), until, firstCursor}, {check.ID, user, since, until, "garbage"},
	} {
		if _, _, err := NetworkHistoryPage(ctx, db, change.id, change.user, change.since, change.until, 2, change.cursor); !errors.Is(err, ErrNetworkCursor) {
			t.Fatalf("unbound cursor accepted: %v", err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := NetworkTrendHistory(canceled, db, check.ID, user, since, until); !errors.Is(err, context.Canceled) {
		t.Fatalf("query ignored cancellation: %v", err)
	}
}
