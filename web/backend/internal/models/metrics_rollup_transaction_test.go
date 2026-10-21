package models

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"server-monitor/internal/testdb"
)

func TestMetricRollupTransactionRestoresSettingsAndRollsBackFailures(t *testing.T) {
	db := testdb.Open(t)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	metricTestExec(t, db, `SET jit=on`)
	var originalPID int
	if err := db.QueryRow(`SELECT pg_backend_pid()`).Scan(&originalPID); err != nil {
		t.Fatal(err)
	}
	wantFailure := errors.New("rollup failed")
	for _, test := range []struct {
		name string
		fail func(*sql.Tx) error
	}{
		{"commit", func(*sql.Tx) error { return nil }},
		{"callback_failure", func(*sql.Tx) error { return wantFailure }},
		{"database_failure", func(tx *sql.Tx) error { _, err := tx.Exec(`SELECT 1/0`); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := withMetricRollupTransaction(context.Background(), db, func(tx *sql.Tx) error {
				var jit, readOnly string
				if err := tx.QueryRow(`SELECT current_setting('jit'),current_setting('transaction_read_only')`).Scan(&jit, &readOnly); err != nil {
					return err
				}
				if jit != "off" || readOnly != "off" {
					t.Fatalf("wrong maintenance settings: jit=%s read_only=%s", jit, readOnly)
				}
				if _, err := tx.Exec(`INSERT INTO metric_maintenance_state(name,completed_at) VALUES($1,now())`, test.name); err != nil {
					return err
				}
				return test.fail(tx)
			})
			if (err == nil) != (test.name == "commit") || test.name == "callback_failure" && !errors.Is(err, wantFailure) {
				t.Fatalf("unexpected transaction result: %v", err)
			}
			var jit string
			var pid, rows int
			if err := db.QueryRow(`SELECT current_setting('jit'),pg_backend_pid(),(SELECT COUNT(*) FROM metric_maintenance_state WHERE name=$1)`, test.name).Scan(&jit, &pid, &rows); err != nil {
				t.Fatal(err)
			}
			if jit != "on" || pid != originalPID || (rows == 1) != (test.name == "commit") {
				t.Fatalf("settings or partial writes leaked: jit=%s pid=%d want=%d rows=%d", jit, pid, originalPID, rows)
			}
		})
	}
}

func TestMetricRollupTransactionCancellationRollsBackAndReleasesConnection(t *testing.T) {
	db := testdb.Open(t)
	db.SetMaxOpenConns(1)
	metricTestExec(t, db, `SET jit=on`)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := withMetricRollupTransaction(ctx, db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO metric_maintenance_state(name,completed_at) VALUES('cancelled_rollup',now())`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `SELECT pg_sleep(10)`)
		return err
	})
	if err == nil || ctx.Err() == nil || time.Since(started) > 2*time.Second {
		t.Fatalf("maintenance outlived cancellation: err=%v context=%v elapsed=%s", err, ctx.Err(), time.Since(started))
	}
	checkCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	var jit string
	var rows int
	if err := db.QueryRowContext(checkCtx, `SELECT current_setting('jit'),(SELECT COUNT(*) FROM metric_maintenance_state WHERE name='cancelled_rollup')`).Scan(&jit, &rows); err != nil || jit != "on" || rows != 0 {
		t.Fatalf("cancelled batch left settings or writes: jit=%s rows=%d err=%v", jit, rows, err)
	}
}

func TestRegularAndRepairRollupsUseLocalSettingsAndPreserveWeightedResults(t *testing.T) {
	db := testdb.Open(t)
	_, server, _ := testdb.Seed(t, db)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	metricTestExec(t, db, `SET jit=on`)
	// Enforce the setting at the actual write, covering both regular rollups
	// and every repair batch, rather than just calling the transaction helper.
	for _, table := range []string{"server_metrics_1m", "server_metrics_15m"} {
		metricTestExec(t, db, "ALTER TABLE "+table+" ADD CONSTRAINT maintenance_jit_disabled CHECK(current_setting('jit')='off')")
	}
	start := time.Now().Add(-time.Hour).UTC().Truncate(15 * time.Minute)
	for _, sample := range []struct {
		cpu float64
		at  time.Time
	}{{10, start.Add(time.Second)}, {90, start.Add(time.Minute + time.Second)}, {90, start.Add(time.Minute + 2*time.Second)}, {90, start.Add(time.Minute + 3*time.Second)}} {
		if err := SaveMetric(db, server, &MetricPoint{CPUPercent: sample.cpu, RecordedAt: sample.at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := RollupMetrics1m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := RollupMetrics15m(db, start, start.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := RepairMetricHistory(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var jit string
	var cpu float64
	var samples int
	if err := db.QueryRow(`SELECT current_setting('jit'),cpu_percent,sample_count FROM server_metrics_15m WHERE server_id=$1 AND recorded_at=$2`, server, start).Scan(&jit, &cpu, &samples); err != nil || jit != "on" || cpu != 70 || samples != 4 {
		t.Fatalf("rollup results/settings changed: jit=%s cpu=%v samples=%d err=%v", jit, cpu, samples, err)
	}
}
