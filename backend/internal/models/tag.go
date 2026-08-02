package models

import (
	"database/sql"
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

func InsertMetric(db *sql.DB, serverID uuid.UUID, m *MetricPoint) error {
	_, err := db.Exec(
		`INSERT INTO server_metrics (server_id, cpu_percent, load_1, load_5, load_15, memory_used, memory_total,
		 disk_used_bytes, network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
		 disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		serverID, m.CPUPercent, m.Load1, m.Load5, m.Load15, m.MemoryUsed, m.MemoryTotal,
		m.DiskUsed, m.NetworkRxBytes, m.NetworkTxBytes, m.NetworkRxTotal, m.NetworkTxTotal,
		m.DiskRxBytes, m.DiskTxBytes, m.UptimeSeconds, m.LatencyMS)
	return err
}

// MaxMetricsPoints limits data points returned to keep charts responsive.
const MaxMetricsPoints = 500

func GetMetrics(db *sql.DB, serverID uuid.UUID, since, until time.Time) ([]MetricPoint, error) {
	// Calculate bucket size so that total points ≤ MaxMetricsPoints.
	bucketSecs := int(until.Sub(since).Seconds()) / MaxMetricsPoints
	if bucketSecs < 1 {
		bucketSecs = 1
	}

	rows, err := db.Query(
		`SELECT AVG(cpu_percent)::DECIMAL(5,2), AVG(load_1)::DECIMAL(8,2), AVG(load_5)::DECIMAL(8,2), AVG(load_15)::DECIMAL(8,2),
		        AVG(memory_used)::BIGINT, AVG(memory_total)::BIGINT, AVG(disk_used_bytes)::BIGINT,
		        AVG(network_rx_bytes)::BIGINT, AVG(network_tx_bytes)::BIGINT,
		        AVG(network_rx_total_bytes)::BIGINT, AVG(network_tx_total_bytes)::BIGINT,
		        AVG(disk_rx_bytes)::BIGINT, AVG(disk_tx_bytes)::BIGINT,
		        AVG(uptime_seconds)::BIGINT, AVG(latency_ms)::INT,
		        TO_TIMESTAMP(FLOOR(EXTRACT(EPOCH FROM recorded_at) / $4) * $4) AS bucket_time
		 FROM server_metrics WHERE server_id=$1 AND recorded_at >= $2 AND recorded_at <= $3
		 GROUP BY bucket_time ORDER BY bucket_time ASC`, serverID, since, until, bucketSecs)
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

func DeleteOldMetrics(db *sql.DB, before time.Time) (int64, error) {
	result, err := db.Exec("DELETE FROM server_metrics WHERE recorded_at < $1", before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func GetLatestMetric(db *sql.DB, serverID uuid.UUID) (*MetricPoint, error) {
	m := &MetricPoint{}
	err := db.QueryRow(
		`SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
		 network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
		 disk_rx_bytes, disk_tx_bytes, uptime_seconds, latency_ms, recorded_at
		 FROM server_metrics WHERE server_id=$1 ORDER BY recorded_at DESC LIMIT 1`, serverID,
	).Scan(&m.CPUPercent, &m.Load1, &m.Load5, &m.Load15, &m.MemoryUsed, &m.MemoryTotal, &m.DiskUsed,
		&m.NetworkRxBytes, &m.NetworkTxBytes, &m.NetworkRxTotal, &m.NetworkTxTotal,
		&m.DiskRxBytes, &m.DiskTxBytes, &m.UptimeSeconds, &m.LatencyMS, &m.RecordedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}
