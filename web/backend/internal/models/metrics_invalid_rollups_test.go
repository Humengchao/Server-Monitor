package models

import (
	"context"
	"testing"
	"time"

	"server-monitor/internal/testdb"
)

func TestMetricRollupRemovesAllInvalidBucketsAndPropagatesToParent(t *testing.T) {
	db := testdb.Open(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(15 * time.Minute)
	// Both raw values are invalid, but their old mean looks valid. A valid-only
	// INSERT has no group and would otherwise leave that misleading 50 behind.
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at) VALUES($1,-1,$2),($1,101,$3)`, server, start.Add(time.Second), start.Add(2*time.Second))
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,50,2,$2)`, server, start)
	metricTestExec(t, db, `INSERT INTO server_metrics_15m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,50,2,$2)`, server, start)
	if err := RollupMetrics1m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics_1m WHERE server_id=$1`, server).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid minute survived: %d %v", count, err)
	}
	if err := RollupMetrics15m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics_15m WHERE server_id=$1`, server).Scan(&count); err != nil || count != 0 {
		t.Fatalf("empty invalid parent survived: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics WHERE server_id=$1`, server).Scan(&count); err != nil || count != 2 {
		t.Fatalf("raw evidence removed: %d %v", count, err)
	}
	points, err := GetMetrics(context.Background(), db, server, start, start.Add(time.Hour))
	if err != nil || len(points) != 0 {
		t.Fatalf("invalid samples became valid/zero history: %+v %v", points, err)
	}
	// A later valid sample rebuilds both previously removed summaries.
	if err := SaveMetric(db, server, &MetricPoint{CPUPercent: 80, RecordedAt: start.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := RollupMetrics1m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := RollupMetrics15m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var cpu float64
	if err := db.QueryRow(`SELECT cpu_percent,sample_count FROM server_metrics_15m WHERE server_id=$1`, server).Scan(&cpu, &count); err != nil || cpu != 80 || count != 1 {
		t.Fatalf("valid late sample lost: cpu=%v count=%d err=%v", cpu, count, err)
	}
}

func TestMetricRollupDoesNotRemoveMissingOrExpiredSourceHistory(t *testing.T) {
	db := testdb.Open(t)
	_, server, _ := testdb.Seed(t, db)
	old := time.Now().UTC().Add(-31 * 24 * time.Hour).Truncate(15 * time.Minute)
	recent := time.Now().UTC().Add(-2 * time.Hour).Truncate(15 * time.Minute)
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at) VALUES($1,-1,$2)`, server, old.Add(time.Second))
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,50,2,$2),($1,70,1,$3)`, server, old, recent)
	metricTestExec(t, db, `INSERT INTO server_metrics_15m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,50,2,$2),($1,70,1,$3)`, server, old, recent)
	if err := RollupMetrics1m(db, old, recent.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics_1m WHERE server_id=$1`, server).Scan(&count); err != nil || count != 2 {
		t.Fatalf("source absence/expiry deleted history: %d %v", count, err)
	}
	// The older source tier itself is invalid, but lies outside its retention.
	metricTestExec(t, db, `UPDATE server_metrics_1m SET cpu_percent=-1 WHERE server_id=$1 AND recorded_at=$2`, server, old)
	if err := RollupMetrics15m(db, old, recent.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics_15m WHERE server_id=$1`, server).Scan(&count); err != nil || count != 2 {
		t.Fatalf("irrecoverable old parent deleted: %d %v", count, err)
	}
}

func TestMetricRepairClearsInvalidMinutesAndReweightsMixedParents(t *testing.T) {
	db := testdb.Open(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(15 * time.Minute)
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at)
	 VALUES($1,-1,$2),($1,101,$3),($1,-1,$4),($1,101,$5)`, server, start.Add(time.Second), start.Add(2*time.Second), start.Add(15*time.Minute+time.Second), start.Add(15*time.Minute+2*time.Second))
	if err := SaveMetric(db, server, &MetricPoint{CPUPercent: 80, RecordedAt: start.Add(16*time.Minute + time.Second)}); err != nil {
		t.Fatal(err)
	}
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,sample_count,recorded_at)
	 VALUES($1,50,2,$2),($1,50,2,$3),($1,80,1,$4)`, server, start, start.Add(15*time.Minute), start.Add(16*time.Minute))
	metricTestExec(t, db, `INSERT INTO server_metrics_15m(server_id,cpu_percent,sample_count,recorded_at)
	 VALUES($1,50,2,$2),($1,60,3,$3)`, server, start, start.Add(15*time.Minute))
	if _, err := RepairMetricHistory(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics_1m WHERE server_id=$1`, server).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid minutes retained: %d %v", count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics_15m WHERE server_id=$1`, server).Scan(&count); err != nil || count != 1 {
		t.Fatalf("invalid parent retained: %d %v", count, err)
	}
	var cpu float64
	if err := db.QueryRow(`SELECT cpu_percent,sample_count FROM server_metrics_15m WHERE server_id=$1`, server).Scan(&cpu, &count); err != nil || cpu != 80 || count != 1 {
		t.Fatalf("mixed parent not reweighted: %v %d %v", cpu, count, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM server_metrics WHERE server_id=$1 AND NOT(cpu_percent BETWEEN 0 AND 100)`, server).Scan(&count); err != nil || count != 4 {
		t.Fatalf("invalid evidence changed: %d %v", count, err)
	}
}
