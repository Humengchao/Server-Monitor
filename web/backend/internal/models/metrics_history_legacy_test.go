package models

import (
	"context"
	"testing"
	"time"

	"server-monitor/internal/testdb"
)

func TestMetricHistoryLegacyPartialRetentionPreservesCoverageWithoutDuplicates(t *testing.T) {
	db := metricHistoryTestDB(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(15 * time.Minute)
	until := start.Add(7 * 24 * time.Hour)
	// The first eight minutes have expired, but their observations still live
	// in an older approximate 15m average. Keeping only seven finer rows would
	// erase that part of the historical population.
	metricTestExec(t, db, `INSERT INTO server_metrics_15m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,50,15,$2)`, server, start)
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,sample_count,recorded_at)
	 SELECT $1,80,1,$2::timestamptz+n*INTERVAL '1 minute' FROM generate_series(8,14)n`, server, start)
	metricTestExec(t, db, `INSERT INTO metric_maintenance_state(name,completed_at) VALUES('weighted_metrics_v2',$1)`, start.Add(15*time.Minute))
	seconds, buckets, err := metricHistoryBuckets(start, until, MaxMetricsPoints)
	if err != nil {
		t.Fatal(err)
	}
	var rows, samples int
	var first time.Time
	if err := db.QueryRow(metricHistorySourceCTE+` SELECT COUNT(*),SUM(samples),MIN(recorded_at) FROM source`, server, start, until, seconds, buckets, time.Now()).Scan(&rows, &samples, &first); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || samples != 15 || !first.Equal(start) {
		t.Fatalf("lost coarse coverage or counted fine rows twice: rows=%d samples=%d first=%v", rows, samples, first)
	}
	points, err := GetMetrics(context.Background(), db, server, start, until)
	if err != nil || len(points) != 1 || points[0].CPUPercent != 50 {
		t.Fatalf("legacy approximation: %+v %v", points, err)
	}
	// A requested edge crossing the retention gap keeps the same coarse
	// approximation, with its chart timestamp clamped into the requested range.
	partialStart := start.Add(2 * time.Minute)
	points, err = GetMetrics(context.Background(), db, server, partialStart, start.Add(12*time.Minute))
	if err != nil || len(points) != 1 || points[0].CPUPercent != 50 || !points[0].RecordedAt.Equal(partialStart) {
		t.Fatalf("partial legacy coverage: %+v %v", points, err)
	}
	// Raw data can fill missing minutes without double-counting the surviving
	// minute rollups. Once the entire population is present, use finer values.
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at)
	 SELECT $1,10,$2::timestamptz+n*INTERVAL '1 minute'+INTERVAL '1 second' FROM generate_series(0,7)n`, server, start)
	if err := db.QueryRow(metricHistorySourceCTE+` SELECT COUNT(*),SUM(samples) FROM source`, server, start, until, seconds, buckets, time.Now()).Scan(&rows, &samples); err != nil {
		t.Fatal(err)
	}
	if rows != 15 || samples != 15 {
		t.Fatalf("complete mixed fine coverage not selected: rows=%d samples=%d", rows, samples)
	}
	points, err = GetMetrics(context.Background(), db, server, start, until)
	if err != nil || len(points) != 1 || points[0].CPUPercent != 42.67 {
		t.Fatalf("fine weighted values: %+v %v", points, err)
	}
}

func TestMetricHistoryLegacyFallbackDoesNotResurrectFilteredInvalidCPU(t *testing.T) {
	db := metricHistoryTestDB(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(15 * time.Minute)
	until := start.Add(7 * 24 * time.Hour)
	// A corrected 1m row has fewer samples because it excluded invalid raw
	// evidence. That is not missing retention coverage: the old 15m value must
	// not be revived simply because its stored sample_count is larger.
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at) VALUES($1,10,$2),($1,-3,$3)`, server, start.Add(time.Second), start.Add(2*time.Second))
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,10,1,$2)`, server, start)
	metricTestExec(t, db, `INSERT INTO server_metrics_15m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,3.5,2,$2)`, server, start)
	points, err := GetMetrics(context.Background(), db, server, start, until)
	if err != nil || len(points) != 1 || points[0].CPUPercent != 10 {
		t.Fatalf("invalid filtering revived contaminated coarse value: %+v %v", points, err)
	}
	// If every original row is invalid, retain no numeric history. This also
	// covers the brief interval before the explicit repair removes old rollups.
	metricTestExec(t, db, `DELETE FROM server_metrics_1m WHERE server_id=$1`, server)
	metricTestExec(t, db, `UPDATE server_metrics SET cpu_percent=101 WHERE server_id=$1 AND cpu_percent=10`, server)
	points, err = GetMetrics(context.Background(), db, server, start, until)
	if err != nil || len(points) != 0 {
		t.Fatalf("all-invalid raw revived old coarse history: %+v %v", points, err)
	}
}

func TestMetricHistoryMinutePartialRawRetentionPreservesCoverage(t *testing.T) {
	db := metricHistoryTestDB(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Minute)
	until := start.Add(time.Hour)
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,50,20,$2)`, server, start)
	// At the raw retention edge only the final eight of twenty three-second
	// measurements remain. A sub-minute display resolution must not discard
	// the minute average that still represents the first twelve observations.
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at)
	 SELECT $1,80,$2::timestamptz+n*INTERVAL '3 seconds' FROM generate_series(12,19)n`, server, start)
	seconds, buckets, err := metricHistoryBuckets(start, until, MaxMetricsPoints)
	if err != nil {
		t.Fatal(err)
	}
	var rows, samples int
	var first time.Time
	if err := db.QueryRow(metricHistorySourceCTE+` SELECT COUNT(*),SUM(samples),MIN(recorded_at) FROM source`, server, start, until, seconds, buckets, time.Now()).Scan(&rows, &samples, &first); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || samples != 20 || !first.Equal(start) {
		t.Fatalf("minute retention lost coverage: rows=%d samples=%d first=%v", rows, samples, first)
	}
	partialStart := start.Add(10 * time.Second)
	points, err := GetMetrics(context.Background(), db, server, partialStart, start.Add(50*time.Second))
	if err != nil || len(points) != 1 || points[0].CPUPercent != 50 || !points[0].RecordedAt.Equal(partialStart) {
		t.Fatalf("partial minute coverage: %+v %v", points, err)
	}
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at)
	 SELECT $1,10,$2::timestamptz+n*INTERVAL '3 seconds' FROM generate_series(0,11)n`, server, start)
	if err := db.QueryRow(metricHistorySourceCTE+` SELECT COUNT(*),SUM(samples) FROM source`, server, start, until, seconds, buckets, time.Now()).Scan(&rows, &samples); err != nil {
		t.Fatal(err)
	}
	if rows != 20 || samples != 20 {
		t.Fatalf("complete raw population not selected exactly once: rows=%d samples=%d", rows, samples)
	}
	// Invalid rows prove coverage even though they do not contribute values.
	// Otherwise an old positive minute average could reappear after filtering.
	metricTestExec(t, db, `UPDATE server_metrics SET cpu_percent=CASE WHEN cpu_percent=10 THEN -1 ELSE 101 END WHERE server_id=$1`, server)
	points, err = GetMetrics(context.Background(), db, server, start, until)
	if err != nil || len(points) != 0 {
		t.Fatalf("invalid raw revived old minute average: %+v %v", points, err)
	}
}

func TestMetricHistoryCoarseFallbackDoesNotDoubleCountInclusiveRawEndpoint(t *testing.T) {
	for _, tier := range []struct {
		table   string
		width   time.Duration
		samples int
	}{
		{"server_metrics_1m", time.Minute, 20},
		{"server_metrics_15m", 15 * time.Minute, 15},
	} {
		t.Run(tier.table, func(t *testing.T) {
			db := metricHistoryTestDB(t)
			_, server, _ := testdb.Seed(t, db)
			start := time.Now().UTC().Add(-48 * time.Hour).Truncate(15 * time.Minute)
			metricTestExec(t, db, `INSERT INTO `+tier.table+`(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,50,$2,$3)`, server, tier.samples, start)
			inside := start.Add(tier.width / 2)
			outside := start.Add(tier.width)
			metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at) VALUES($1,80,$2),($1,100,$3)`, server, inside, outside)
			for _, window := range []struct {
				until         time.Time
				rows, samples int
			}{
				{inside, 1, tier.samples},
				{outside, 2, tier.samples + 1},
			} {
				seconds, buckets, err := metricHistoryBuckets(start, window.until, MaxMetricsPoints)
				if err != nil {
					t.Fatal(err)
				}
				var rows, samples int
				if err := db.QueryRow(metricHistorySourceCTE+` SELECT COUNT(*),SUM(samples) FROM source`, server, start, window.until, seconds, buckets, time.Now()).Scan(&rows, &samples); err != nil {
					t.Fatal(err)
				}
				if rows != window.rows || samples != window.samples {
					t.Fatalf("until %v: rows=%d samples=%d want rows=%d samples=%d", window.until, rows, samples, window.rows, window.samples)
				}
			}
		})
	}
}
