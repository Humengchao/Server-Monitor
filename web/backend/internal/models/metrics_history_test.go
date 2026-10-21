package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func metricTestExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func metricHistoryTestDB(t *testing.T) *sql.DB {
	db := testdb.Open(t)
	// Raw schema predates rollup defaults. Bulk fixtures below supply complete
	// samples, like SaveMetric, while allowing each test to name only its signal.
	metricTestExec(t, db, `ALTER TABLE server_metrics
	 ALTER memory_used SET DEFAULT 0, ALTER memory_total SET DEFAULT 0,
	 ALTER network_rx_bytes SET DEFAULT 0, ALTER network_tx_bytes SET DEFAULT 0,
	 ALTER uptime_seconds SET DEFAULT 0`)
	return db
}

func TestMetricHistoryStrictPointLimitAndInclusiveEndpoints(t *testing.T) {
	db := metricHistoryTestDB(t)
	_, server, _ := testdb.Seed(t, db)
	since := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Add(123456 * time.Microsecond)
	until := since.Add(time.Hour)
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at)
	 SELECT $1,CASE WHEN n=3600 THEN 100 ELSE 10 END,$2::timestamptz+n*INTERVAL '1 second' FROM generate_series(0,3600)n`, server, since)
	points, err := GetMetrics(context.Background(), db, server, since, until)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 450 || !points[0].RecordedAt.Equal(since) || points[len(points)-1].CPUPercent != 20 {
		t.Fatalf("wrong relative buckets/endpoints: count=%d first=%+v last=%+v", len(points), points[0], points[len(points)-1])
	}
	for i, point := range points {
		if point.RecordedAt.Before(since) || point.RecordedAt.After(until) || (i > 0 && !point.RecordedAt.After(points[i-1].RecordedAt)) {
			t.Fatalf("out-of-range/duplicate point: %+v", point)
		}
	}
}

func TestMetricHistoryWeightedTiersFillHolesAndLateTail(t *testing.T) {
	db := metricHistoryTestDB(t)
	user, server, _ := testdb.Seed(t, db)
	now := time.Now().UTC().Truncate(time.Minute)
	since := now.Add(-24 * time.Hour)
	// Coarse values are deliberately different from duplicate raw rows: the
	// old part must come from 1m, while missing buckets and a stale recent
	// minute must use raw, including a late sample after its rollup was made.
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,memory_used,sample_count,recorded_at)
	 VALUES($1,10,100,1,$2),($1,90,900,3,$3),($1,10,100,1,$4)`, server, since.Add(15*time.Minute), since.Add(16*time.Minute), now.Add(-2*time.Minute))
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,memory_used,recorded_at)
	 VALUES($1,0,0,$2),($1,50,500,$3),($1,10,100,$4),($1,90,900,$5)`, server,
		since.Add(15*time.Minute+time.Second), since.Add(17*time.Minute+time.Second), now.Add(-2*time.Minute+time.Second), now.Add(-2*time.Minute+2*time.Second))
	points, err := GetMetrics(context.Background(), db, server, since, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 || points[0].CPUPercent != 66 || points[0].MemoryUsed != 660 || points[1].CPUPercent != 50 {
		t.Fatalf("tier overlap/gap/weight/tail: %+v", points)
	}
	ai, err := GetAIPanelHistory(context.Background(), db, user, server, now, "24h", 1)
	if err != nil {
		t.Fatal(err)
	}
	var point map[string]any
	if len(ai.Points) != 1 {
		t.Fatalf("AI points: %+v", ai)
	}
	if err := json.Unmarshal(ai.Points[0], &point); err != nil {
		t.Fatal(err)
	}
	if point["samples"] != float64(7) || point["cpu_average_percent"] != 61.43 {
		t.Fatalf("AI diverged: %s", ai.Points[0])
	}
}

func TestMetricHistoryPartialEdgesUseRawAndMissingTiersFallback(t *testing.T) {
	db := metricHistoryTestDB(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Minute)
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,0,2,$2)`, server, start)
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at) VALUES($1,0,$2),($1,80,$3),($1,100,$4)`, server, start, start.Add(30*time.Second), start.Add(time.Minute))
	points, err := GetMetrics(context.Background(), db, server, start.Add(20*time.Second), start.Add(time.Minute))
	if err != nil || len(points) != 2 || points[0].CPUPercent != 80 || points[1].CPUPercent != 100 {
		t.Fatalf("edge samples: %+v %v", points, err)
	}
	metricTestExec(t, db, `DELETE FROM server_metrics WHERE server_id=$1`, server)
	points, err = GetMetrics(context.Background(), db, server, start.Add(20*time.Second), start.Add(time.Minute))
	if err != nil || len(points) != 1 || points[0].CPUPercent != 0 || !points[0].RecordedAt.Equal(start.Add(20*time.Second)) {
		t.Fatalf("retained edge fallback: %+v %v", points, err)
	}
	metricTestExec(t, db, `DELETE FROM server_metrics_1m WHERE server_id=$1`, server)
	points, err = GetMetrics(context.Background(), db, server, start, start.Add(time.Hour))
	if err != nil || points == nil || len(points) != 0 {
		t.Fatalf("empty tiers: %+v %v", points, err)
	}
}

func TestMetricHistoryLegacyFifteenMinuteUsesSurvivingMinutes(t *testing.T) {
	db := metricHistoryTestDB(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Now().UTC().Add(-48 * time.Hour).Truncate(15 * time.Minute)
	metricTestExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,10,1,$2),($1,90,3,$3)`, server, start, start.Add(time.Minute))
	metricTestExec(t, db, `INSERT INTO server_metrics_15m(server_id,cpu_percent,sample_count,recorded_at) VALUES($1,50,4,$2)`, server, start)
	points, err := GetMetrics(context.Background(), db, server, start, start.Add(7*24*time.Hour))
	if err != nil || len(points) != 1 || points[0].CPUPercent != 70 {
		t.Fatalf("legacy average leaked: %+v %v", points, err)
	}
	if err := RollupMetrics15m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	metricTestExec(t, db, `UPDATE metric_maintenance_state SET completed_at=$1 WHERE name='weighted_metrics_v2'`, start)
	// A conflicting raw duplicate proves the marked 15m tier is selected.
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at) VALUES($1,0,$2)`, server, start)
	points, err = GetMetrics(context.Background(), db, server, start, start.Add(7*24*time.Hour))
	if err != nil || len(points) != 1 || points[0].CPUPercent != 70 {
		t.Fatalf("weighted 15m: %+v %v", points, err)
	}
	metricTestExec(t, db, `DELETE FROM server_metrics_1m WHERE server_id=$1`, server)
	metricTestExec(t, db, `DELETE FROM server_metrics WHERE server_id=$1`, server)
	metricTestExec(t, db, `DELETE FROM metric_maintenance_state WHERE name='weighted_metrics_v2'`)
	points, err = GetMetrics(context.Background(), db, server, start, start.Add(time.Hour))
	if err != nil || len(points) != 1 || points[0].CPUPercent != 70 {
		t.Fatalf("old-only tier unavailable: %+v %v", points, err)
	}
}

func TestMetricRollupWeightsAndRejectsInvalidCPUWithoutZeroFill(t *testing.T) {
	db := metricHistoryTestDB(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(15 * time.Minute)
	for _, cpu := range []float64{-1, 101, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if err := SaveMetric(db, server, &MetricPoint{CPUPercent: cpu, RecordedAt: start}); err == nil {
			t.Fatalf("accepted invalid CPU %v", cpu)
		}
	}
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,memory_used,recorded_at) VALUES
	 ($1,10,100,$2),($1,-3,9999,$3),($1,90,900,$4),($1,90,900,$5),($1,90,900,$6)`, server, start.Add(time.Second), start.Add(2*time.Second), start.Add(time.Minute+time.Second), start.Add(time.Minute+2*time.Second), start.Add(time.Minute+3*time.Second))
	if err := RollupMetrics1m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := RollupMetrics15m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var cpu float64
	var samples int
	var memory int64
	if err := db.QueryRow(`SELECT cpu_percent,sample_count,memory_used FROM server_metrics_15m WHERE server_id=$1`, server).Scan(&cpu, &samples, &memory); err != nil {
		t.Fatal(err)
	}
	if cpu != 70 || samples != 4 || memory != 700 {
		t.Fatalf("bad weighted rollup: CPU %v samples %d memory %d", cpu, samples, memory)
	}
	// The caller's partial range cannot overwrite an existing complete minute.
	if err := RollupMetrics1m(db, start.Add(time.Second), start.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := db.QueryRow(`SELECT SUM(sample_count) FROM server_metrics_1m WHERE server_id=$1`, server).Scan(&total); err != nil || total != 4 {
		t.Fatalf("partial overwrite: %d %v", total, err)
	}
}

func TestMetricRetentionBatchFairnessCutoffAndConcurrentCleaners(t *testing.T) {
	db := metricHistoryTestDB(t)
	user, server, _ := testdb.Seed(t, db)
	other := uuid.New()
	metricTestExec(t, db, `INSERT INTO servers(id,user_id,name,host,monitoring_enabled) VALUES($1,$2,'offline','192.0.2.2',FALSE)`, other, user)
	before := time.Now().UTC().Truncate(time.Second)
	for _, table := range []string{"server_metrics", "server_metrics_1m", "server_metrics_15m"} {
		metricTestExec(t, db, `INSERT INTO `+table+`(server_id,recorded_at) SELECT $1,$2::timestamptz-n*INTERVAL '1 minute' FROM generate_series(0,20)n`, server, before)
		metricTestExec(t, db, `INSERT INTO `+table+`(server_id,recorded_at) SELECT $1,$2::timestamptz-n*INTERVAL '1 minute' FROM generate_series(0,2)n`, other, before)
		deleted, err := DeleteMetricBatch(db, table, before, 6)
		if err != nil || deleted != 6 {
			t.Fatalf("%s underfilled: %d %v", table, deleted, err)
		}
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE server_id=$1 AND recorded_at<$2`, other, before).Scan(&count); err != nil || count != 0 {
			t.Fatalf("offline server starved: %d %v", count, err)
		}
		var wg sync.WaitGroup
		errors := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _, err := DeleteMetricBatch(db, table, before, 10); errors <- err }()
		}
		wg.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				t.Fatal(err)
			}
		}
		for {
			n, err := DeleteMetricBatch(db, table, before, 10)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				break
			}
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 2 {
			t.Fatalf("cutoff deleted or backlog survived: %d %v", count, err)
		}
	}
}

func TestMetricHistoryDailyReadsRollupsAndPreservesAllSamples(t *testing.T) {
	db := metricHistoryTestDB(t)
	_, server, _ := testdb.Seed(t, db)
	until := time.Now().UTC().Truncate(time.Minute)
	since := until.Add(-24 * time.Hour)
	metricTestExec(t, db, `INSERT INTO server_metrics(server_id,cpu_percent,recorded_at)
	 SELECT $1,10,$2::timestamptz+n*INTERVAL '3 seconds' FROM generate_series(0,28800)n`, server, since)
	if err := RollupMetrics1m(db, since, until); err != nil {
		t.Fatal(err)
	}
	metricTestExec(t, db, `ANALYZE server_metrics`)
	metricTestExec(t, db, `ANALYZE server_metrics_1m`)
	seconds, buckets, err := metricHistoryBuckets(since, until, MaxMetricsPoints)
	if err != nil {
		t.Fatal(err)
	}
	var rows, samples int
	if err := db.QueryRow(metricHistorySourceCTE+` SELECT COUNT(*),SUM(samples) FROM source`, server, since, until, seconds, buckets, until).Scan(&rows, &samples); err != nil {
		t.Fatal(err)
	}
	if rows > 1600 || samples != 28801 {
		t.Fatalf("daily selected rows=%d represented samples=%d", rows, samples)
	}
	var raw []byte
	if err := db.QueryRow("EXPLAIN (ANALYZE, FORMAT JSON) "+metricHistorySourceCTE+` SELECT COUNT(*) FROM source`, server, since, until, seconds, buckets, until).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	var rawRows float64
	var walk func(any)
	walk = func(node any) {
		m, ok := node.(map[string]any)
		if !ok {
			return
		}
		if m["Relation Name"] == "server_metrics" {
			r, _ := m["Actual Rows"].(float64)
			loops, _ := m["Actual Loops"].(float64)
			removed, _ := m["Rows Removed by Filter"].(float64)
			rawRows += (r + removed) * loops
		}
		children, _ := m["Plans"].([]any)
		for _, child := range children {
			walk(child)
		}
	}
	walk(plans[0]["Plan"])
	if rawRows > 3000 {
		t.Fatalf("daily query still scanned too much raw history: %.0f\n%s", rawRows, raw)
	}
	t.Logf("daily history: 28,801 represented samples, %d selected rows, %.0f raw tuples examined, execution %v ms", rows, rawRows, plans[0]["Execution Time"])
}
