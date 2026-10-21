package models

import (
	"fmt"
	"math"
	"time"
)

// History is bounded to the longest retained tier. Bucket boundaries are relative
// to since, and the inclusive until endpoint belongs to the final bucket.
const MaxMetricHistoryWindow = 366 * 24 * time.Hour

func metricHistoryBuckets(since, until time.Time, limit int) (int, int, error) {
	span := until.Sub(since)
	if since.IsZero() || until.IsZero() || span <= 0 || span > MaxMetricHistoryWindow || limit < 1 || limit > MaxMetricsPoints {
		return 0, 0, fmt.Errorf("invalid metric history range")
	}
	seconds := math.Max(1, math.Ceil(span.Seconds()/float64(limit)))
	return int(seconds), int(math.Ceil(span.Seconds() / seconds)), nil
}

const metricHistoryProjection = `m.cpu_percent,m.load_1,m.load_5,m.load_15,m.memory_used,m.memory_total,m.disk_used_bytes,
 m.network_rx_bytes,m.network_tx_bytes,m.network_rx_total_bytes,m.network_tx_total_bytes,
 m.disk_rx_bytes,m.disk_tx_bytes,m.uptime_seconds,m.latency_ms,m.recorded_at`

// Parameters: server, since, until, bucket seconds, bucket count, observed now.
// Coarse rows cover disjoint intervals; indexed lateral reads fill only their
// gaps with finer rows. This avoids scanning a day's raw data merely to discard
// it after joining a rollup. Recent overlaps stay raw/1m so late arrivals are
// visible before the next maintenance pass. Empty tiers and internal missing
// buckets naturally fall back to finer data.
//
// Legacy 15m rows are replaced by finer data only when that data still represents
// the coarse bucket's sample population. At a retention boundary, using just the
// surviving minutes would silently drop the earlier part of that bucket. Count
// all raw rows (including invalid CPU) as coverage evidence, and take the larger
// raw/1m count per minute: filtering invalid values must not resurrect an older,
// contaminated coarse average. When finer coverage is incomplete, retain the
// coarse approximation without also counting its remaining fine rows. Partial
// edges beyond finer retention use that same approximation, clamped to since.
var metricHistorySourceCTE = fmt.Sprintf(`WITH fifteen AS MATERIALIZED (
 SELECT %s,m.sample_count::bigint AS samples
 FROM server_metrics_15m m
 WHERE m.server_id=$1 AND m.recorded_at >= $2::timestamptz-INTERVAL '15 minutes'
 AND m.recorded_at < $3 AND m.recorded_at+INTERVAL '15 minutes'>$2
 AND m.cpu_percent BETWEEN 0 AND 100 AND m.sample_count>0
 AND (
   ($4>=900 AND m.recorded_at>=$2 AND m.recorded_at+INTERVAL '15 minutes'<=$3
    AND m.recorded_at+INTERVAL '15 minutes'<=$6::timestamptz-INTERVAL '1 hour'
    AND m.recorded_at >= (SELECT completed_at FROM metric_maintenance_state WHERE name='weighted_metrics_v2'))
   OR (SELECT CASE WHEN fine.samples>=m.sample_count THEN FALSE ELSE
       fine.samples+COALESCE((
         SELECT SUM(GREATEST(raw.samples-COALESCE(GREATEST(f.sample_count,0),0),0))
         FROM (
           SELECT TO_TIMESTAMP(FLOOR(EXTRACT(EPOCH FROM recorded_at)/60)*60) AS minute_at,COUNT(*) AS samples
           FROM server_metrics WHERE server_id=$1
           AND recorded_at>=m.recorded_at AND recorded_at<m.recorded_at+INTERVAL '15 minutes'
           GROUP BY FLOOR(EXTRACT(EPOCH FROM recorded_at)/60)
         ) raw LEFT JOIN server_metrics_1m f ON f.server_id=$1 AND f.recorded_at=raw.minute_at
       ),0)<m.sample_count END
       FROM (SELECT COALESCE(SUM(GREATEST(f.sample_count,0)),0) AS samples
         FROM server_metrics_1m f WHERE f.server_id=$1
         AND f.recorded_at>=m.recorded_at AND f.recorded_at<m.recorded_at+INTERVAL '15 minutes') fine)
 )
), fifteen_edges AS (
 SELECT GREATEST(recorded_at,$2) AS start_at,LEAST(recorded_at+INTERVAL '15 minutes',$3) AS end_at FROM fifteen
), fifteen_gaps AS (
 SELECT COALESCE(LAG(end_at) OVER(ORDER BY start_at),$2) AS start_at,start_at AS end_at FROM fifteen_edges
 UNION ALL SELECT COALESCE(MAX(end_at),$2),$3 FROM fifteen_edges
), minute AS MATERIALIZED (
 SELECT %s,m.sample_count::bigint AS samples
 FROM fifteen_gaps g CROSS JOIN LATERAL (
   SELECT * FROM server_metrics_1m m WHERE m.server_id=$1
   AND m.recorded_at>=g.start_at-INTERVAL '1 minute' AND m.recorded_at<g.end_at
   AND m.recorded_at+INTERVAL '1 minute'>g.start_at
   AND m.cpu_percent BETWEEN 0 AND 100 AND m.sample_count>0
   AND (($4>=60 AND m.recorded_at>=$2 AND m.recorded_at+INTERVAL '1 minute'<=$3
        AND m.recorded_at+INTERVAL '1 minute'<=$6::timestamptz-INTERVAL '5 minutes')
     OR (SELECT COUNT(*) FROM server_metrics f WHERE f.server_id=$1
       AND f.recorded_at>=m.recorded_at AND f.recorded_at<m.recorded_at+INTERVAL '1 minute')<m.sample_count)
   OFFSET 0
 ) m WHERE g.start_at<g.end_at
), covered AS (
 SELECT start_at,end_at FROM fifteen_edges
 UNION ALL SELECT GREATEST(recorded_at,$2),LEAST(recorded_at+INTERVAL '1 minute',$3) FROM minute
), raw_gaps AS (
 SELECT COALESCE(LAG(end_at) OVER(ORDER BY start_at),$2) AS start_at,start_at AS end_at FROM covered
 UNION ALL SELECT COALESCE(MAX(end_at),$2),$3 FROM covered
), source AS (
 SELECT * FROM fifteen UNION ALL SELECT * FROM minute
 UNION ALL
 SELECT %s,1::bigint AS samples FROM raw_gaps g CROSS JOIN LATERAL (
   SELECT * FROM server_metrics m WHERE m.server_id=$1 AND m.recorded_at>=g.start_at AND m.recorded_at<g.end_at
   AND m.cpu_percent BETWEEN 0 AND 100 OFFSET 0
 ) m WHERE g.start_at<g.end_at
 UNION ALL SELECT %s,1::bigint AS samples FROM server_metrics m
 WHERE m.server_id=$1 AND m.recorded_at=$3 AND m.cpu_percent BETWEEN 0 AND 100
 AND NOT EXISTS(SELECT 1 FROM fifteen f WHERE f.recorded_at<=$3 AND f.recorded_at+INTERVAL '15 minutes'>$3)
 AND NOT EXISTS(SELECT 1 FROM minute f WHERE f.recorded_at<=$3 AND f.recorded_at+INTERVAL '1 minute'>$3)
), bucketed AS (
 SELECT *,LEAST(GREATEST(FLOOR(EXTRACT(EPOCH FROM(recorded_at-$2::timestamptz))/$4)::int,0),$5-1) AS bucket FROM source
)`, metricHistoryProjection, metricHistoryProjection, metricHistoryProjection, metricHistoryProjection)
