package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"server-monitor/internal/testdb"
)

func TestMetricHistoryReadSettingsAreLocalToReadOnlyTransaction(t *testing.T) {
	db := testdb.Open(t)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	metricTestExec(t, db, `SET jit=on`)
	var pid int
	if err := db.QueryRow(`SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	checkRestored := func() {
		t.Helper()
		var jit string
		var actualPID int
		if err := db.QueryRow(`SELECT current_setting('jit'),pg_backend_pid()`).Scan(&jit, &actualPID); err != nil {
			t.Fatal(err)
		}
		if jit != "on" || actualPID != pid {
			t.Fatalf("pooled connection changed/leaked settings: jit=%s pid=%d want=%d", jit, actualPID, pid)
		}
	}
	wantFailure := errors.New("read failed")
	for _, failure := range []error{nil, wantFailure} {
		err := readMetricHistory(context.Background(), db, func(tx *sql.Tx) error {
			var jit, readOnly string
			if err := tx.QueryRow(`SELECT current_setting('jit'),current_setting('transaction_read_only')`).Scan(&jit, &readOnly); err != nil {
				return err
			}
			if jit != "off" || readOnly != "on" {
				t.Fatalf("wrong query settings: jit=%s read_only=%s", jit, readOnly)
			}
			return failure
		})
		if !errors.Is(err, failure) {
			t.Fatalf("callback result changed: %v want %v", err, failure)
		}
		checkRestored()
	}
	if err := readMetricHistory(context.Background(), db, func(tx *sql.Tx) error { _, err := tx.Exec(`SELECT 1/0`); return err }); err == nil {
		t.Fatal("database failure ignored")
	}
	checkRestored()
}

func TestMetricHistoryReadCancellationReleasesConnection(t *testing.T) {
	db := testdb.Open(t)
	db.SetMaxOpenConns(1)
	metricTestExec(t, db, `SET jit=on`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := readMetricHistory(ctx, db, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, `SELECT pg_sleep(10)`); return err })
	if err == nil || ctx.Err() == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("query outlived cancellation: %v context=%v duration=%s", err, ctx.Err(), time.Since(started))
	}
	checkCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	var jit string
	if err := db.QueryRowContext(checkCtx, `SHOW jit`).Scan(&jit); err != nil || jit != "on" {
		t.Fatalf("connection not reusable after cancellation: jit=%s err=%v", jit, err)
	}
}

func TestMetricAndAIHistoryPreserveResultsAndPooledSettings(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	metricTestExec(t, db, `SET jit=on`)
	var originalPID int
	if err := db.QueryRow(`SELECT pg_backend_pid()`).Scan(&originalPID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	for i, cpu := range []float64{10, 90} {
		if err := SaveMetric(db, server, &MetricPoint{CPUPercent: cpu, RecordedAt: now.Add(-time.Minute + time.Duration(i+1)*time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	points, err := GetMetrics(context.Background(), db, server, now.Add(-time.Hour), now)
	if err != nil || len(points) != 1 || points[0].CPUPercent != 50 {
		t.Fatalf("ordinary history changed: %+v %v", points, err)
	}
	ai, err := GetAIPanelHistory(context.Background(), db, user, server, now, "1h", 1)
	if err != nil || len(ai.Points) != 1 {
		t.Fatalf("AI history changed: %+v %v", ai, err)
	}
	var point map[string]any
	if err := json.Unmarshal(ai.Points[0], &point); err != nil {
		t.Fatal(err)
	}
	if point["samples"] != float64(2) || point["cpu_average_percent"] != float64(50) {
		t.Fatalf("AI values changed: %s", ai.Points[0])
	}
	var jit string
	var pid int
	if err := db.QueryRow(`SELECT current_setting('jit'),pg_backend_pid()`).Scan(&jit, &pid); err != nil || jit != "on" || pid != originalPID {
		t.Fatalf("history leaked connection settings: jit=%s pid=%d want=%d err=%v", jit, pid, originalPID, err)
	}
}
