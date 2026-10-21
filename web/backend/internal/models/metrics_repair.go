package models

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const MetricRepairTimeout = 5 * time.Minute
const metricRepairRetentionMargin = 2 * time.Minute

type MetricRepairRange struct {
	Since        time.Time `json:"since"`
	Until        time.Time `json:"until"`
	BatchSeconds int       `json:"batch_seconds"`
}

type MetricRepairPlan struct {
	RawToMinute     MetricRepairRange `json:"raw_to_minute"`
	MinuteToFifteen MetricRepairRange `json:"minute_to_fifteen"`
}

type MetricRepairResult struct {
	Plan                 MetricRepairPlan `json:"plan"`
	MinuteBatches        int              `json:"minute_batches"`
	FifteenMinuteBatches int              `json:"fifteen_minute_batches"`
	WeightedSince        *time.Time       `json:"weighted_since,omitempty"`
}

func metricCeilTime(value time.Time, width time.Duration) time.Time {
	aligned := value.Truncate(width)
	if aligned.Before(value) {
		return aligned.Add(width)
	}
	return aligned
}

// PlanMetricRepair includes only complete buckets whose source rows remain
// inside retention. The first two minutes are deliberately left untouched so
// concurrent cleanup cannot remove part of the first source bucket.
func PlanMetricRepair(now time.Time) MetricRepairPlan {
	now = now.UTC()
	return MetricRepairPlan{
		RawToMinute:     MetricRepairRange{Since: metricCeilTime(now.Add(-24*time.Hour+metricRepairRetentionMargin), time.Minute), Until: now.Truncate(time.Minute), BatchSeconds: 3600},
		MinuteToFifteen: MetricRepairRange{Since: metricCeilTime(now.Add(-30*24*time.Hour+metricRepairRetentionMargin), 15*time.Minute), Until: now.Truncate(15 * time.Minute), BatchSeconds: 86400},
	}
}

// RepairMetricHistory is an explicit, bounded operator action; it is never run
// at startup. It leaves raw evidence and older, irrecoverable rollups intact.
// Every batch commits independently, so a failed run can safely be retried.
// The historical weighting marker is moved only after all batches succeed.
func RepairMetricHistory(ctx context.Context, db *sql.DB) (MetricRepairResult, error) {
	ctx, cancel := context.WithTimeout(ctx, MetricRepairTimeout)
	defer cancel()
	result := MetricRepairResult{Plan: PlanMetricRepair(time.Now())}
	batch := func(source, target string, width int, samples string, since, until time.Time) error {
		batchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return withMetricRollupTransaction(batchCtx, db, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(batchCtx, `SET LOCAL statement_timeout='10s'`); err != nil {
				return err
			}
			if _, err := tx.ExecContext(batchCtx, `SET LOCAL lock_timeout='1s'`); err != nil {
				return err
			}
			return rollupMetricsWithExecutor(batchCtx, tx, source, target, width, samples, since, until)
		})
	}
	raw := result.Plan.RawToMinute
	for since := raw.Since; since.Before(raw.Until); {
		until := since.Add(time.Hour)
		if until.After(raw.Until) {
			until = raw.Until
		}
		if err := batch("server_metrics", "server_metrics_1m", 60, "COUNT(*)::INT", since, until); err != nil {
			return result, fmt.Errorf("repair 1m batch %s: %w", since.Format(time.RFC3339), err)
		}
		result.MinuteBatches++
		since = until
	}
	// The raw phase may take several minutes. Recheck the oldest retained
	// minute boundary immediately before starting this phase, rather than
	// using a stale dry-run cutoff that cleanup may already have passed.
	fifteen := result.Plan.MinuteToFifteen
	if fresh := PlanMetricRepair(time.Now()).MinuteToFifteen.Since; fresh.After(fifteen.Since) {
		fifteen.Since = fresh
	}
	result.Plan.MinuteToFifteen = fifteen
	for since := fifteen.Since; since.Before(fifteen.Until); {
		until := since.Add(24 * time.Hour)
		if until.After(fifteen.Until) {
			until = fifteen.Until
		}
		if err := batch("server_metrics_1m", "server_metrics_15m", 900, "SUM(sample_count)::INT", since, until); err != nil {
			return result, fmt.Errorf("repair 15m batch %s: %w", since.Format(time.RFC3339), err)
		}
		result.FifteenMinuteBatches++
		since = until
	}
	markerCtx, stopMarker := context.WithTimeout(ctx, 10*time.Second)
	defer stopMarker()
	_, err := db.ExecContext(markerCtx, `INSERT INTO metric_maintenance_state(name,completed_at) VALUES('weighted_metrics_v2',$1)
	 ON CONFLICT(name) DO UPDATE SET completed_at=LEAST(metric_maintenance_state.completed_at,EXCLUDED.completed_at)`, fifteen.Since)
	if err != nil {
		return result, fmt.Errorf("mark repaired interval: %w", err)
	}
	result.WeightedSince = &fifteen.Since
	return result, nil
}
