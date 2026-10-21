package models

import (
	"context"
	"fmt"
	"testing"
	"time"

	"server-monitor/internal/testdb"
)

func TestMetricRepairPlanClipsRetentionAndIncompleteBuckets(t *testing.T) {
	now := time.Date(2026, 10, 22, 10, 7, 31, 123, time.UTC)
	plan := PlanMetricRepair(now)
	if want := time.Date(2026, 10, 21, 10, 10, 0, 0, time.UTC); !plan.RawToMinute.Since.Equal(want) {
		t.Fatalf("raw starts at %v, want %v", plan.RawToMinute.Since, want)
	}
	if !plan.RawToMinute.Until.Equal(now.Truncate(time.Minute)) || !plan.MinuteToFifteen.Until.Equal(now.Truncate(15*time.Minute)) {
		t.Fatalf("partial last buckets included: %+v", plan)
	}
	if want := time.Date(2026, 9, 22, 10, 15, 0, 0, time.UTC); !plan.MinuteToFifteen.Since.Equal(want) {
		t.Fatalf("minute starts at %v, want %v", plan.MinuteToFifteen.Since, want)
	}
	for _, r := range []MetricRepairRange{plan.RawToMinute, plan.MinuteToFifteen} {
		if !r.Since.Before(r.Until) {
			t.Fatalf("empty repair: %+v", r)
		}
	}
}

func TestMetricRepairFailedLaterBatchDoesNotAdvanceWeightingMarker(t *testing.T) {
	db := testdb.Open(t)
	_, server, _ := testdb.Seed(t, db)
	plan := PlanMetricRepair(time.Now())
	cutoff := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	metricTestExec(t, db, `INSERT INTO metric_maintenance_state(name,completed_at) VALUES('weighted_metrics_v2',$1)`, cutoff)
	for _, at := range []time.Time{plan.RawToMinute.Since.Add(30 * time.Minute), plan.RawToMinute.Since.Add(90 * time.Minute)} {
		if err := SaveMetric(db, server, &MetricPoint{CPUPercent: 10, RecordedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	// The first hourly batch commits. The next one fails a database constraint;
	// a partial repair must leave the historical weighting cutoff unchanged.
	metricTestExec(t, db, fmt.Sprintf(`ALTER TABLE server_metrics_1m ADD CONSTRAINT reject_second_hour CHECK(recorded_at<'%s'::timestamptz)`, plan.RawToMinute.Since.Add(time.Hour).Format(time.RFC3339)))
	result, err := RepairMetricHistory(context.Background(), db)
	if err == nil || result.MinuteBatches != 1 || result.FifteenMinuteBatches != 0 || result.WeightedSince != nil {
		t.Fatalf("partial repair result: %+v %v", result, err)
	}
	var actual time.Time
	if err := db.QueryRow(`SELECT completed_at FROM metric_maintenance_state WHERE name='weighted_metrics_v2'`).Scan(&actual); err != nil || !actual.Equal(cutoff) {
		t.Fatalf("premature marker move: %v %v", actual, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics_1m WHERE server_id=$1`, server).Scan(&count); err != nil || count != 1 {
		t.Fatalf("first batch not committed: %d %v", count, err)
	}
}

func TestMetricRepairPreservesEvidenceAndUnrecoverableHistory(t *testing.T) {
	db := testdb.Open(t)
	_, server, _ := testdb.Seed(t, db)
	plan := PlanMetricRepair(time.Now())
	old := plan.MinuteToFifteen.Since.Add(-24 * time.Hour)
	recent := plan.RawToMinute.Since.Add(time.Hour).Truncate(15 * time.Minute)
	for _, sample := range []struct {
		cpu float64
		at  time.Time
	}{{10, recent.Add(time.Second)}, {90, recent.Add(time.Minute + time.Second)}, {90, recent.Add(time.Minute + 2*time.Second)}, {90, recent.Add(time.Minute + 3*time.Second)}} {
		if err := SaveMetric(db, server, &MetricPoint{CPUPercent: sample.cpu, RecordedAt: sample.at}); err != nil {
			t.Fatal(err)
		}
	}
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at) VALUES($1,-3,$2)`, server, recent.Add(2*time.Second))
	metricTestExec(t, db, `INSERT INTO server_metrics_15m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,77,4,$2),($1,50,4,$3)`, server, old, recent)
	result, err := RepairMetricHistory(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if result.MinuteBatches != 24 || result.FifteenMinuteBatches != 30 || result.WeightedSince == nil {
		t.Fatalf("incomplete repair: %+v", result)
	}
	var cpu float64
	if err := db.QueryRow(`SELECT cpu_percent FROM server_metrics_15m WHERE server_id=$1 AND recorded_at=$2`, server, recent).Scan(&cpu); err != nil || cpu != 70 {
		t.Fatalf("retained repair: %v %v", cpu, err)
	}
	if err := db.QueryRow(`SELECT cpu_percent FROM server_metrics_15m WHERE server_id=$1 AND recorded_at=$2`, server, old).Scan(&cpu); err != nil || cpu != 77 {
		t.Fatalf("old history changed: %v %v", cpu, err)
	}
	var invalid int
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics WHERE server_id=$1 AND cpu_percent<0`, server).Scan(&invalid); err != nil || invalid != 1 {
		t.Fatalf("raw evidence removed: %v %v", invalid, err)
	}
	var cutoff time.Time
	if err := db.QueryRow(`SELECT completed_at FROM metric_maintenance_state WHERE name='weighted_metrics_v2'`).Scan(&cutoff); err != nil || !cutoff.Equal(result.Plan.MinuteToFifteen.Since) {
		t.Fatalf("wrong certified interval: %v %v", cutoff, err)
	}
	// A retry does not move a previously certified cutoff later or lose data.
	if _, err := RepairMetricHistory(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}
