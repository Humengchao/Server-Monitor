package models

import (
	"context"
	"fmt"
	"time"
)

// An INSERT over valid samples has no group for an entirely invalid bucket.
// Remove its old average explicitly, but only when source evidence survives
// and the complete bucket lies safely inside that source's retention window.
// Absence of source data alone never authorizes removing historical rollups.
func clearInvalidMetricRollups(ctx context.Context, executor metricRollupExecutor, source, target string, width time.Duration, since, until time.Time) error {
	retention := 24 * time.Hour
	validCount := ""
	parentEvidence := ""
	if source == "server_metrics_1m" {
		retention = 30 * 24 * time.Hour
		validCount = " AND f.sample_count>0"
		// The preceding raw->1m repair may already have removed every minute
		// in this parent. Raw invalid rows provide evidence that it must also
		// be cleared, without deleting the raw records themselves.
		parentEvidence = ` OR (t.recorded_at >= $5
		 AND EXISTS(SELECT 1 FROM server_metrics r WHERE r.server_id=t.server_id
		   AND r.recorded_at>=t.recorded_at AND r.recorded_at<t.recorded_at+$3*INTERVAL '1 second')
		 AND NOT EXISTS(SELECT 1 FROM server_metrics r WHERE r.server_id=t.server_id
		   AND r.recorded_at>=t.recorded_at AND r.recorded_at<t.recorded_at+$3*INTERVAL '1 second'
		   AND r.cpu_percent BETWEEN 0 AND 100))`
	} else {
		// Keep the same argument shape for both source tiers.
		parentEvidence = " OR (FALSE AND t.recorded_at >= $5)"
	}
	now := time.Now()
	safeSourceStart := metricCeilTime(now.Add(-retention+metricRepairRetentionMargin), width)
	safeRawStart := metricCeilTime(now.Add(-24*time.Hour+metricRepairRetentionMargin), width)
	query := fmt.Sprintf(`WITH doomed AS MATERIALIZED (
	 SELECT t.ctid FROM servers s CROSS JOIN LATERAL (
	   SELECT t.ctid FROM %s t WHERE t.server_id=s.id
	   AND t.recorded_at>=GREATEST($1::timestamptz,$4::timestamptz) AND t.recorded_at<$2
	   AND NOT EXISTS(SELECT 1 FROM %s f WHERE f.server_id=t.server_id
	     AND f.recorded_at>=t.recorded_at AND f.recorded_at<t.recorded_at+$3*INTERVAL '1 second'
	     AND f.cpu_percent BETWEEN 0 AND 100 %s)
	   AND (EXISTS(SELECT 1 FROM %s f WHERE f.server_id=t.server_id
	     AND f.recorded_at>=t.recorded_at AND f.recorded_at<t.recorded_at+$3*INTERVAL '1 second') %s)
	   OFFSET 0
	 ) t
	) DELETE FROM %s WHERE ctid IN(SELECT ctid FROM doomed)`, target, source, validCount, source, parentEvidence, target)
	_, err := executor.ExecContext(ctx, query, since, until, int(width/time.Second), safeSourceStart, safeRawStart)
	return err
}
