package models

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func TestMetricRollupPreservesUnchangedRowsAndRepairsLateSamples(t *testing.T) {
	db := testdb.Open(t)
	_, server, _ := testdb.Seed(t, db)
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	add := func(cpu float64, at time.Time) {
		t.Helper()
		if err := SaveMetric(db, server, &MetricPoint{CPUPercent: cpu, MemoryTotal: 1024, RecordedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	rollup := func() {
		t.Helper()
		if err := RollupMetrics1m(db, start, start.Add(15*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := RollupMetrics15m(db, start, start.Add(15*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	version := func(table string) string {
		t.Helper()
		var xmin string
		if err := db.QueryRow("SELECT xmin::text FROM "+table+" WHERE server_id=$1 AND recorded_at=$2", server, start).Scan(&xmin); err != nil {
			t.Fatal(err)
		}
		return xmin
	}
	add(10, start.Add(time.Second))
	rollup()
	one, fifteen := version("server_metrics_1m"), version("server_metrics_15m")
	rollup()
	if version("server_metrics_1m") != one || version("server_metrics_15m") != fifteen {
		t.Fatal("unchanged rollup wrote a new row version")
	}
	add(90, start.Add(2*time.Second))
	rollup()
	for _, table := range []string{"server_metrics_1m", "server_metrics_15m"} {
		var cpu float64
		var samples int
		if err := db.QueryRow("SELECT cpu_percent,sample_count FROM "+table+" WHERE server_id=$1", server).Scan(&cpu, &samples); err != nil {
			t.Fatal(err)
		}
		if cpu != 50 || samples != 2 {
			t.Fatalf("%s did not repair late samples: cpu=%v count=%d", table, cpu, samples)
		}
		if _, err := db.Exec("UPDATE "+table+" SET cpu_percent=NULL WHERE server_id=$1", server); err != nil {
			t.Fatal(err)
		}
	}
	rollup()
	var cpu float64
	if err := db.QueryRow("SELECT cpu_percent FROM server_metrics_15m WHERE server_id=$1", server).Scan(&cpu); err != nil || cpu != 50 {
		t.Fatalf("NULL repair: %v %v", cpu, err)
	}
}

func TestMetricRollupCursorIncludesOfflineServersAndHandlesEmptyHistory(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	fallback := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	latest := fallback.Add(24 * time.Hour)
	other := uuid.New()
	if _, err := db.Exec(`INSERT INTO servers(id,user_id,name,host,monitoring_enabled,is_public) VALUES($1,$2,'paused','192.0.2.2',FALSE,FALSE)`, other, user); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"server_metrics_1m", "server_metrics_15m"} {
		got, err := MetricRollupStart(db, table, fallback, time.Hour)
		if err != nil || !got.Equal(fallback) {
			t.Fatalf("empty tier: %v %v", got, err)
		}
		if _, err := db.Exec("INSERT INTO "+table+"(server_id,recorded_at) VALUES($1,$2),($3,$4)", server, latest.Add(-2*time.Hour), other, latest); err != nil {
			t.Fatal(err)
		}
		got, err = MetricRollupStart(db, table, fallback, time.Hour)
		if err != nil || !got.Equal(latest.Add(-time.Hour)) {
			t.Fatalf("paused host excluded: %v %v", got, err)
		}
	}
	if _, err := db.Exec("DELETE FROM servers WHERE id=$1", other); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"server_metrics_1m", "server_metrics_15m"} {
		got, err := MetricRollupStart(db, table, fallback, time.Hour)
		if err != nil || !got.Equal(latest.Add(-3*time.Hour)) {
			t.Fatalf("deleted host retained: %v %v", got, err)
		}
		got, err = MetricRollupStart(db, table, latest, 48*time.Hour)
		if err != nil || !got.Equal(latest) {
			t.Fatalf("fallback not clamped: %v %v", got, err)
		}
	}
	if _, err := MetricRollupStart(db, "users", fallback, time.Hour); err == nil {
		t.Fatal("unapproved table accepted")
	}
}
