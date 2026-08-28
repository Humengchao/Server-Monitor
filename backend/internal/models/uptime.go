package models

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// UptimeCounts is the raw bucket census for one server, before it is turned
// into percentages. CreatedAt is carried along because a window has to be
// clamped to it.
type UptimeCounts struct {
	ServerID  uuid.UUID
	CreatedAt time.Time
	// Minute-tier bucket counts, used for the 24-hour and 7-day windows.
	Minute24h int
	Minute7d  int
	// Fifteen-minute-tier count, used for the 30-day window.
	Quarter30d int
}

// UptimeCensusRange is the half-open range each tier is counted over. The two
// tiers get different ends because each is snapped to its own bucket width (see
// services.TierEnd), and passing five bare timestamps positionally was one
// transposition away from a silently wrong percentage.
type UptimeCensusRange struct {
	Since24h time.Time
	Since7d  time.Time
	// Until1m bounds the one-minute tier, which serves the 24h and 7d windows.
	Until1m  time.Time
	Since30d time.Time
	// Until15m bounds the fifteen-minute tier, which serves the 30d window.
	Until15m time.Time
}

// GetFleetUptimeCounts censuses metric buckets for all of a user's servers.
//
// Both queries are pure index range scans: server_metrics_1m and
// server_metrics_15m are keyed on (server_id, recorded_at), so counting a
// window never touches the heap. Reading the 30-day figure from the coarser
// tier keeps that count at ~2880 entries per server instead of ~43200.
//
// Each lower bound is raised to the server's creation time, because a host's
// first sample lands in the bucket already in progress and is therefore stamped
// *before* it was created. Counting that row against a window that starts at the
// first boundary after creation inflates a new server past 100%.
func GetFleetUptimeCounts(db *sql.DB, userID uuid.UUID, r UptimeCensusRange) ([]UptimeCounts, error) {
	rows, err := db.Query(
		`SELECT s.id, s.created_at,
			COUNT(m.recorded_at) FILTER (WHERE m.recorded_at >= GREATEST($2, s.created_at)) AS minute_24h,
			COUNT(m.recorded_at) AS minute_7d
		 FROM servers s
		 LEFT JOIN server_metrics_1m m
			ON m.server_id = s.id AND m.recorded_at >= GREATEST($3, s.created_at) AND m.recorded_at < $4
		 WHERE s.user_id = $1
		 GROUP BY s.id, s.created_at`,
		userID, r.Since24h, r.Since7d, r.Until1m)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byID := make(map[uuid.UUID]*UptimeCounts)
	order := make([]uuid.UUID, 0)
	for rows.Next() {
		var item UptimeCounts
		if err := rows.Scan(&item.ServerID, &item.CreatedAt, &item.Minute24h, &item.Minute7d); err != nil {
			return nil, err
		}
		copied := item
		byID[item.ServerID] = &copied
		order = append(order, item.ServerID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	quarterRows, err := db.Query(
		`SELECT s.id, COUNT(m.recorded_at)
		 FROM servers s
		 LEFT JOIN server_metrics_15m m
			ON m.server_id = s.id AND m.recorded_at >= GREATEST($2, s.created_at) AND m.recorded_at < $3
		 WHERE s.user_id = $1
		 GROUP BY s.id`,
		userID, r.Since30d, r.Until15m)
	if err != nil {
		return nil, err
	}
	defer quarterRows.Close()
	for quarterRows.Next() {
		var id uuid.UUID
		var count int
		if err := quarterRows.Scan(&id, &count); err != nil {
			return nil, err
		}
		if entry, ok := byID[id]; ok {
			entry.Quarter30d = count
		}
	}
	if err := quarterRows.Err(); err != nil {
		return nil, err
	}

	result := make([]UptimeCounts, 0, len(order))
	for _, id := range order {
		result = append(result, *byID[id])
	}
	return result, nil
}

// DailyAvailability is one calendar day's bucket census, for the daily strip.
type DailyAvailability struct {
	Day      time.Time `json:"day"`
	Observed int       `json:"observed_buckets"`
}

// DailyStripBucketSeconds is the tier the daily strip reads.
//
// It deliberately matches the 30-day window's tier rather than using the finer
// one-minute rollup, for two reasons: the strip then agrees with the 30-day
// percentage printed next to it, and it is immune to the one-minute tier's
// retention boundary — that tier keeps exactly 30 days, so the oldest day of a
// 30-day strip would otherwise show a phantom dip as rows aged out. A quarter
// hour of resolution is invisible on a bar representing a whole day.
const DailyStripBucketSeconds = 15 * 60

// GetServerCreatedAt is needed on its own for the per-server detail, which does
// not go through the fleet census.
func GetServerCreatedAt(db *sql.DB, serverID, userID uuid.UUID) (time.Time, error) {
	var createdAt time.Time
	err := db.QueryRow(
		`SELECT created_at FROM servers WHERE id=$1 AND user_id=$2`, serverID, userID).Scan(&createdAt)
	return createdAt, err
}

// GetDailyAvailability returns the per-day bucket count for one server from the
// fifteen-minute tier (see DailyStripBucketSeconds). Days with no samples at all
// are absent from the result; the caller fills the calendar so a fully-down day
// still shows up in the strip.
func GetDailyAvailability(db *sql.DB, serverID uuid.UUID, since, until time.Time) ([]DailyAvailability, error) {
	rows, err := db.Query(
		`SELECT date_trunc('day', recorded_at) AS day, COUNT(*)
		 FROM server_metrics_15m
		 WHERE server_id=$1 AND recorded_at >= $2 AND recorded_at < $3
		 GROUP BY day ORDER BY day`,
		serverID, since, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	days := make([]DailyAvailability, 0)
	for rows.Next() {
		var item DailyAvailability
		if err := rows.Scan(&item.Day, &item.Observed); err != nil {
			return nil, err
		}
		days = append(days, item)
	}
	return days, rows.Err()
}

// GetMetricGaps returns only the adjacent bucket pairs that are further apart
// than gapSeconds. Finding them with a window function server-side means the
// full bucket list (tens of thousands of rows) never crosses the wire.
func GetMetricGaps(db *sql.DB, serverID uuid.UUID, since, until time.Time, gapSeconds, limit int) ([]MetricGapRow, error) {
	rows, err := db.Query(
		`SELECT prev_at, recorded_at FROM (
			SELECT recorded_at, LAG(recorded_at) OVER (ORDER BY recorded_at) AS prev_at
			FROM server_metrics_1m
			WHERE server_id=$1 AND recorded_at >= $2 AND recorded_at < $3
		 ) g
		 WHERE prev_at IS NOT NULL
		   AND recorded_at - prev_at > make_interval(secs => $4::int)
		 ORDER BY prev_at DESC
		 LIMIT $5`,
		serverID, since, until, gapSeconds, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gaps := make([]MetricGapRow, 0)
	for rows.Next() {
		var gap MetricGapRow
		if err := rows.Scan(&gap.Before, &gap.After); err != nil {
			return nil, err
		}
		gaps = append(gaps, gap)
	}
	return gaps, rows.Err()
}

// MetricGapRow mirrors services.MetricGap; kept separate so the models package
// does not import services.
type MetricGapRow struct {
	Before time.Time
	After  time.Time
}

// GetLastMetricBucket returns the newest minute bucket for a server, or the
// zero time when it has never reported. This is what lets an ongoing outage be
// detected: there is no later bucket to pair the gap with.
func GetLastMetricBucket(db *sql.DB, serverID uuid.UUID, until time.Time) (time.Time, error) {
	var last sql.NullTime
	err := db.QueryRow(
		`SELECT MAX(recorded_at) FROM server_metrics_1m WHERE server_id=$1 AND recorded_at < $2`,
		serverID, until).Scan(&last)
	if err != nil {
		return time.Time{}, err
	}
	if !last.Valid {
		return time.Time{}, nil
	}
	return last.Time, nil
}
