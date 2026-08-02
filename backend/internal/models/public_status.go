package models

import (
	"database/sql"
	"time"
)

// PublicServerMetric is deliberately separate from Server. This query never
// selects names, hosts, ports, credentials, user IDs, notes or database IDs.
type PublicServerMetric struct {
	ServerType      string
	CPUCores        int
	DiskTotal       int64
	ExpiresAt       *time.Time
	BillingPrice    float64
	BillingCurrency string
	BillingCycle    string
	TrafficLimit    int64
	CPUPercent      float64
	Load1           float64
	Load5           float64
	Load15          float64
	MemoryUsed      int64
	MemoryTotal     int64
	DiskUsed        int64
	NetworkRxBytes  int64
	NetworkTxBytes  int64
	NetworkRxTotal  int64
	NetworkTxTotal  int64
	UptimeSeconds   int64
	LatencyMS       int
	RecordedAt      *time.Time
}

func GetPublicServerMetrics(db *sql.DB) ([]PublicServerMetric, error) {
	rows, err := db.Query(`
		SELECT COALESCE(s.server_type, 'linux'), COALESCE(s.cpu_cores, 0), COALESCE(s.disk_total_bytes, 0),
			s.expires_at, COALESCE(s.billing_price, 0), COALESCE(s.billing_currency, 'CNY'),
			COALESCE(s.billing_cycle, 'year'), COALESCE(s.traffic_limit_bytes, 0),
			COALESCE(sm.cpu_percent, 0), COALESCE(sm.load_1, 0), COALESCE(sm.load_5, 0), COALESCE(sm.load_15, 0),
			COALESCE(sm.memory_used, 0), COALESCE(sm.memory_total, 0), COALESCE(sm.disk_used_bytes, 0),
			COALESCE(sm.network_rx_bytes, 0), COALESCE(sm.network_tx_bytes, 0),
			COALESCE(sm.network_rx_total_bytes, 0), COALESCE(sm.network_tx_total_bytes, 0),
			COALESCE(sm.uptime_seconds, 0), COALESCE(sm.latency_ms, 0), sm.recorded_at
		FROM servers s
		LEFT JOIN LATERAL (
			SELECT cpu_percent, load_1, load_5, load_15, memory_used, memory_total, disk_used_bytes,
				network_rx_bytes, network_tx_bytes, network_rx_total_bytes, network_tx_total_bytes,
				uptime_seconds, latency_ms, recorded_at
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
		var expiresAt, recordedAt sql.NullTime
		if err := rows.Scan(
			&item.ServerType, &item.CPUCores, &item.DiskTotal, &expiresAt,
			&item.BillingPrice, &item.BillingCurrency, &item.BillingCycle, &item.TrafficLimit,
			&item.CPUPercent, &item.Load1, &item.Load5, &item.Load15,
			&item.MemoryUsed, &item.MemoryTotal, &item.DiskUsed,
			&item.NetworkRxBytes, &item.NetworkTxBytes, &item.NetworkRxTotal, &item.NetworkTxTotal,
			&item.UptimeSeconds, &item.LatencyMS, &recordedAt,
		); err != nil {
			return nil, err
		}
		if expiresAt.Valid {
			item.ExpiresAt = &expiresAt.Time
		}
		if recordedAt.Valid {
			item.RecordedAt = &recordedAt.Time
		}
		metrics = append(metrics, item)
	}
	return metrics, rows.Err()
}
