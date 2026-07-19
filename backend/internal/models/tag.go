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
	MemoryUsed     int64     `json:"memory_used"`
	MemoryTotal    int64     `json:"memory_total"`
	NetworkRxBytes int64     `json:"network_rx_bytes"`
	NetworkTxBytes int64     `json:"network_tx_bytes"`
	DiskRxBytes    int64     `json:"disk_rx_bytes"`
	DiskTxBytes    int64     `json:"disk_tx_bytes"`
	UptimeSeconds  int64     `json:"uptime_seconds"`
	RecordedAt     time.Time `json:"recorded_at"`
}

func InsertMetric(db *sql.DB, serverID uuid.UUID, m *MetricPoint) error {
	_, err := db.Exec(
		`INSERT INTO server_metrics (server_id, cpu_percent, memory_used, memory_total, network_rx_bytes, network_tx_bytes, disk_rx_bytes, disk_tx_bytes, uptime_seconds)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		serverID, m.CPUPercent, m.MemoryUsed, m.MemoryTotal, m.NetworkRxBytes, m.NetworkTxBytes, m.DiskRxBytes, m.DiskTxBytes, m.UptimeSeconds)
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
		`SELECT AVG(cpu_percent)::DECIMAL(5,2), AVG(memory_used)::BIGINT, AVG(memory_total)::BIGINT,
		        AVG(network_rx_bytes)::BIGINT, AVG(network_tx_bytes)::BIGINT,
		        AVG(disk_rx_bytes)::BIGINT, AVG(disk_tx_bytes)::BIGINT,
		        AVG(uptime_seconds)::BIGINT,
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
		if err := rows.Scan(&m.CPUPercent, &m.MemoryUsed, &m.MemoryTotal,
			&m.NetworkRxBytes, &m.NetworkTxBytes, &m.DiskRxBytes, &m.DiskTxBytes, &m.UptimeSeconds, &m.RecordedAt); err != nil {
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
		`SELECT cpu_percent, memory_used, memory_total, network_rx_bytes, network_tx_bytes, disk_rx_bytes, disk_tx_bytes, uptime_seconds, recorded_at
		 FROM server_metrics WHERE server_id=$1 ORDER BY recorded_at DESC LIMIT 1`, serverID,
	).Scan(&m.CPUPercent, &m.MemoryUsed, &m.MemoryTotal, &m.NetworkRxBytes, &m.NetworkTxBytes, &m.DiskRxBytes, &m.DiskTxBytes, &m.UptimeSeconds, &m.RecordedAt)
	if err != nil {
		return nil, err
	}
	return m, nil
}
