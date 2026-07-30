package models

import (
	"database/sql"
	"time"
)

// PublicServerMetric is deliberately separate from Server. The public query
// never selects hostnames, addresses, credentials, user IDs, notes or database
// IDs, so private fields cannot accidentally be serialized by the handler.
type PublicServerMetric struct {
	ServerType    string
	CPUCores      int
	CPUPercent    float64
	MemoryUsed    int64
	MemoryTotal   int64
	UptimeSeconds int64
	RecordedAt    *time.Time
}

func GetPublicServerMetrics(db *sql.DB) ([]PublicServerMetric, error) {
	rows, err := db.Query(`
		SELECT COALESCE(s.server_type, 'linux'), COALESCE(s.cpu_cores, 0),
			COALESCE(sm.cpu_percent, 0), COALESCE(sm.memory_used, 0),
			COALESCE(sm.memory_total, 0), COALESCE(sm.uptime_seconds, 0),
			sm.recorded_at
		FROM servers s
		LEFT JOIN LATERAL (
			SELECT cpu_percent, memory_used, memory_total, uptime_seconds, recorded_at
			FROM server_metrics
			WHERE server_id = s.id
			ORDER BY recorded_at DESC
			LIMIT 1
		) sm ON true
		ORDER BY s.created_at ASC, s.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	metrics := make([]PublicServerMetric, 0)
	for rows.Next() {
		var item PublicServerMetric
		var recordedAt sql.NullTime
		if err := rows.Scan(
			&item.ServerType,
			&item.CPUCores,
			&item.CPUPercent,
			&item.MemoryUsed,
			&item.MemoryTotal,
			&item.UptimeSeconds,
			&recordedAt,
		); err != nil {
			return nil, err
		}
		if recordedAt.Valid {
			item.RecordedAt = &recordedAt.Time
		}
		metrics = append(metrics, item)
	}
	return metrics, rows.Err()
}
