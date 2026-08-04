package models

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Tag struct {
	ID     uuid.UUID `json:"id"`
	UserID uuid.UUID `json:"user_id"`
	Name   string    `json:"name"`
	Color  string    `json:"color"`
}

func CreateTag(db *sql.DB, t *Tag) error {
	t.ID = uuid.New()
	_, err := db.Exec(
		"INSERT INTO tags (id, user_id, name, color) VALUES ($1,$2,$3,$4)",
		t.ID, t.UserID, t.Name, t.Color)
	return err
}

func GetTagsByUserID(db *sql.DB, userID uuid.UUID) ([]Tag, error) {
	rows, err := db.Query("SELECT id, user_id, name, color FROM tags WHERE user_id=$1 ORDER BY name", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Color); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, nil
}

func DeleteTag(db *sql.DB, id, userID uuid.UUID) error {
	_, err := db.Exec("DELETE FROM tags WHERE id=$1 AND user_id=$2", id, userID)
	return err
}

type MetricPoint struct {
	CPUPercent     float64   `json:"cpu_percent"`
	Load1          float64   `json:"load_1"`
	Load5          float64   `json:"load_5"`
	Load15         float64   `json:"load_15"`
	MemoryUsed     int64     `json:"memory_used"`
	MemoryTotal    int64     `json:"memory_total"`
	DiskUsed       int64     `json:"disk_used"`
	NetworkRxBytes int64     `json:"network_rx_bytes"`
	NetworkTxBytes int64     `json:"network_tx_bytes"`
	NetworkRxTotal int64     `json:"network_rx_total_bytes"`
	NetworkTxTotal int64     `json:"network_tx_total_bytes"`
	DiskRxBytes    int64     `json:"disk_rx_bytes"`
	DiskTxBytes    int64     `json:"disk_tx_bytes"`
	UptimeSeconds  int64     `json:"uptime_seconds"`
	LatencyMS      int       `json:"latency_ms"`
	RecordedAt     time.Time `json:"recorded_at"`
}

// SaveMetric atomically updates the one-row latest table and appends the raw
// three-second sample used by the 24-hour history tier.
func SaveMetric(db *sql.DB, serverID uuid.UUID, m *MetricPoint) error {
	if m.RecordedAt.IsZero() {
		m.RecordedAt = time.Now()
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`INSERT INTO server_latest_metrics (server_id, cpu_percent, load_1, load_5, load_15, memory_used, memory_total,
		 disk_used_bytes, network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
		 disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		 ON CONFLICT (server_id) DO UPDATE SET
		 cpu_percent=EXCLUDED.cpu_percent, load_1=EXCLUDED.load_1, load_5=EXCLUDED.load_5, load_15=EXCLUDED.load_15,
		 memory_used=EXCLUDED.memory_used, memory_total=EXCLUDED.memory_total, disk_used_bytes=EXCLUDED.disk_used_bytes,
		 network_rx_bytes=EXCLUDED.network_rx_bytes, network_tx_bytes=EXCLUDED.network_tx_bytes,
		 network_rx_total_bytes=EXCLUDED.network_rx_total_bytes, network_tx_total_bytes=EXCLUDED.network_tx_total_bytes,
		 disk_rx_bytes=EXCLUDED.disk_rx_bytes, disk_tx_bytes=EXCLUDED.disk_tx_bytes,
		 uptime_seconds=EXCLUDED.uptime_seconds, latency_ms=EXCLUDED.latency_ms, recorded_at=EXCLUDED.recorded_at`,
		serverID, m.CPUPercent, m.Load1, m.Load5, m.Load15, m.MemoryUsed, m.MemoryTotal,
		m.DiskUsed, m.NetworkRxBytes, m.NetworkTxBytes, m.NetworkRxTotal, m.NetworkTxTotal,
		m.DiskRxBytes, m.DiskTxBytes, m.UptimeSeconds, m.LatencyMS, m.RecordedAt)
	if err != nil {
		return err
	}

	_, err = tx.Exec(
		`INSERT INTO server_metrics (server_id, cpu_percent, load_1, load_5, load_15, memory_used, memory_total,
		 disk_used_bytes, network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
		 disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		serverID, m.CPUPercent, m.Load1, m.Load5, m.Load15, m.MemoryUsed, m.MemoryTotal,
		m.DiskUsed, m.NetworkRxBytes, m.NetworkTxBytes, m.NetworkRxTotal, m.NetworkTxTotal,
		m.DiskRxBytes, m.DiskTxBytes, m.UptimeSeconds, m.LatencyMS, m.RecordedAt)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// MaxMetricsPoints limits data points returned to keep charts responsive.
const MaxMetricsPoints = 500

func GetMetrics(db *sql.DB, serverID uuid.UUID, since, until time.Time) ([]MetricPoint, error) {
	// Calculate bucket size so that total points ≤ MaxMetricsPoints.
	bucketSecs := int(until.Sub(since).Seconds()) / MaxMetricsPoints
	if bucketSecs < 1 {
		bucketSecs = 1
	}

	now := time.Now()
	rawCutoff := now.Add(-24 * time.Hour)
	minuteCutoff := now.AddDate(0, 0, -30)
	rows, err := db.Query(
		`WITH source AS (
			SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
				network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
				disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
			FROM server_metrics_15m
			WHERE server_id=$1 AND recorded_at >= $2 AND recorded_at <= $3 AND recorded_at < $6
			UNION ALL
			SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
				network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
				disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
			FROM server_metrics_1m
			WHERE server_id=$1 AND recorded_at >= $2 AND recorded_at <= $3 AND recorded_at >= $6 AND recorded_at < $5
			UNION ALL
			SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
				network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
				disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
			FROM server_metrics
			WHERE server_id=$1 AND recorded_at >= $2 AND recorded_at <= $3 AND recorded_at >= $5
		)
		SELECT AVG(cpu_percent)::DECIMAL(5,2), AVG(load_1)::DECIMAL(8,2), AVG(load_5)::DECIMAL(8,2), AVG(load_15)::DECIMAL(8,2),
		       ROUND(AVG(memory_used))::BIGINT, MAX(memory_total)::BIGINT, ROUND(AVG(disk_used_bytes))::BIGINT,
		       ROUND(AVG(network_rx_bytes))::BIGINT, ROUND(AVG(network_tx_bytes))::BIGINT,
		       MAX(network_rx_total_bytes)::BIGINT, MAX(network_tx_total_bytes)::BIGINT,
		       ROUND(AVG(disk_rx_bytes))::BIGINT, ROUND(AVG(disk_tx_bytes))::BIGINT,
		       MAX(uptime_seconds)::BIGINT, ROUND(AVG(latency_ms))::INT,
		       TO_TIMESTAMP(FLOOR(EXTRACT(EPOCH FROM recorded_at) / $4) * $4) AS bucket_time
		FROM source
		GROUP BY bucket_time ORDER BY bucket_time ASC`, serverID, since, until, bucketSecs, rawCutoff, minuteCutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []MetricPoint
	for rows.Next() {
		var m MetricPoint
		if err := rows.Scan(&m.CPUPercent, &m.Load1, &m.Load5, &m.Load15, &m.MemoryUsed, &m.MemoryTotal, &m.DiskUsed,
			&m.NetworkRxBytes, &m.NetworkTxBytes, &m.NetworkRxTotal, &m.NetworkTxTotal,
			&m.DiskRxBytes, &m.DiskTxBytes, &m.UptimeSeconds, &m.LatencyMS, &m.RecordedAt); err != nil {
			return nil, err
		}
		points = append(points, m)
	}
	if points == nil {
		points = []MetricPoint{}
	}
	return points, nil
}

func GetLatestMetric(db *sql.DB, serverID uuid.UUID) (*MetricPoint, error) {
	m := &MetricPoint{}
	err := db.QueryRow(
		`SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
		 network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
		 disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
		 FROM server_latest_metrics WHERE server_id=$1`, serverID,
	).Scan(&m.CPUPercent, &m.Load1, &m.Load5, &m.Load15, &m.MemoryUsed, &m.MemoryTotal, &m.DiskUsed,
		&m.NetworkRxBytes, &m.NetworkTxBytes, &m.NetworkRxTotal, &m.NetworkTxTotal,
		&m.DiskRxBytes, &m.DiskTxBytes, &m.UptimeSeconds, &m.LatencyMS, &m.RecordedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func RollupMetrics1m(db *sql.DB, since, until time.Time) error {
	return rollupMetrics(db, "server_metrics", "server_metrics_1m", 60, "COUNT(*)::INT", since, until)
}

func RollupMetrics15m(db *sql.DB, since, until time.Time) error {
	return rollupMetrics(db, "server_metrics_1m", "server_metrics_15m", 15*60, "COALESCE(SUM(sample_count), 0)::INT", since, until)
}

func rollupMetrics(db *sql.DB, sourceTable, targetTable string, bucketSeconds int, sampleExpression string, since, until time.Time) error {
	allowed := (sourceTable == "server_metrics" && targetTable == "server_metrics_1m") ||
		(sourceTable == "server_metrics_1m" && targetTable == "server_metrics_15m")
	if !allowed {
		return fmt.Errorf("unsupported metric rollup %s -> %s", sourceTable, targetTable)
	}
	query := fmt.Sprintf(`INSERT INTO %s (server_id, cpu_percent, load_1, load_5, load_15, memory_used, memory_total,
		 disk_used_bytes, network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
		 disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, sample_count, recorded_at)
		 SELECT server_id, AVG(cpu_percent)::DECIMAL(5,2), AVG(load_1)::DECIMAL(8,2), AVG(load_5)::DECIMAL(8,2), AVG(load_15)::DECIMAL(8,2),
		 ROUND(AVG(memory_used))::BIGINT, MAX(memory_total)::BIGINT, ROUND(AVG(disk_used_bytes))::BIGINT,
		 ROUND(AVG(network_rx_bytes))::BIGINT, ROUND(AVG(network_tx_bytes))::BIGINT,
		 MAX(network_rx_total_bytes)::BIGINT, MAX(network_tx_total_bytes)::BIGINT,
		 ROUND(AVG(disk_rx_bytes))::BIGINT, ROUND(AVG(disk_tx_bytes))::BIGINT,
		 MAX(uptime_seconds)::BIGINT, ROUND(AVG(latency_ms))::INT, %s,
		 TO_TIMESTAMP(FLOOR(EXTRACT(EPOCH FROM recorded_at) / $3) * $3)
		 FROM %s WHERE recorded_at >= $1 AND recorded_at < $2
		 GROUP BY server_id, FLOOR(EXTRACT(EPOCH FROM recorded_at) / $3)
		 ON CONFLICT (server_id, recorded_at) DO UPDATE SET
		 cpu_percent=EXCLUDED.cpu_percent, load_1=EXCLUDED.load_1, load_5=EXCLUDED.load_5, load_15=EXCLUDED.load_15,
		 memory_used=EXCLUDED.memory_used, memory_total=EXCLUDED.memory_total, disk_used_bytes=EXCLUDED.disk_used_bytes,
		 network_rx_bytes=EXCLUDED.network_rx_bytes, network_tx_bytes=EXCLUDED.network_tx_bytes,
		 network_rx_total_bytes=EXCLUDED.network_rx_total_bytes, network_tx_total_bytes=EXCLUDED.network_tx_total_bytes,
		 disk_rx_bytes=EXCLUDED.disk_rx_bytes, disk_tx_bytes=EXCLUDED.disk_tx_bytes,
		 uptime_seconds=EXCLUDED.uptime_seconds, latency_ms=EXCLUDED.latency_ms, sample_count=EXCLUDED.sample_count`,
		targetTable, sampleExpression, sourceTable)
	_, err := db.Exec(query, since, until, bucketSeconds)
	return err
}

func MetricRollupBackfilled(db *sql.DB) (bool, error) {
	var exists bool
	err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM metric_maintenance_state WHERE name='tiered_metrics_v1')`).Scan(&exists)
	return exists, err
}

func MarkMetricRollupBackfilled(db *sql.DB) error {
	_, err := db.Exec(`INSERT INTO metric_maintenance_state (name, completed_at) VALUES ('tiered_metrics_v1', NOW())
		ON CONFLICT (name) DO UPDATE SET completed_at=EXCLUDED.completed_at`)
	return err
}

func MetricRollupStart(db *sql.DB, table string, fallback time.Time, overlap time.Duration) (time.Time, error) {
	switch table {
	case "server_metrics_1m", "server_metrics_15m":
	default:
		return time.Time{}, fmt.Errorf("unsupported rollup table %q", table)
	}
	var latest sql.NullTime
	if err := db.QueryRow(fmt.Sprintf("SELECT MAX(recorded_at) FROM %s", table)).Scan(&latest); err != nil {
		return time.Time{}, err
	}
	if !latest.Valid {
		return fallback, nil
	}
	start := latest.Time.Add(-overlap)
	if start.Before(fallback) {
		return fallback, nil
	}
	return start, nil
}

// DeleteMetricBatch bounds locks, WAL bursts and dead tuples created by
// retention cleanup. Table names are selected internally and never user input.
func DeleteMetricBatch(db *sql.DB, table string, before time.Time, limit int) (int64, error) {
	switch table {
	case "server_metrics", "server_metrics_1m", "server_metrics_15m":
	default:
		return 0, fmt.Errorf("unsupported metric table %q", table)
	}
	query := fmt.Sprintf(`WITH doomed AS (
		SELECT ctid FROM %s WHERE recorded_at < $1 LIMIT $2
	) DELETE FROM %s WHERE ctid IN (SELECT ctid FROM doomed)`, table, table)
	result, err := db.Exec(query, before, limit)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
