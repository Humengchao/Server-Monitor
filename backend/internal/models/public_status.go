package models

import (
	"database/sql"
	"encoding/json"
	"time"
)

type PublicTag struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// PublicServerMetric is deliberately separate from Server. Names, manually
// configured public locations and tags are intentionally public; hosts, ports,
// credentials, user IDs, notes and database IDs are still excluded.
type PublicServerMetric struct {
	Name            string
	PublicLocation  string
	Tags            []PublicTag
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
	// Observed 30-day availability, derived from the fifteen-minute rollup.
	// Availability30dObserved/Expected are bucket counts; the handler turns them
	// into a percentage so the arithmetic lives in one place.
	Availability30dObserved int
	Availability30dExpected int
}

// GetPublicServerMetrics also censuses the fifteen-minute rollup so the status
// page can publish a 30-day availability figure. The window start is clamped to
// each server's creation time, otherwise a host added yesterday would advertise
// a 3% SLA.
func GetPublicServerMetrics(db *sql.DB, since30d, until time.Time) ([]PublicServerMetric, error) {
	rows, err := db.Query(`
		SELECT s.name, COALESCE(s.public_location, ''),
			COALESCE((
				SELECT JSON_AGG(JSON_BUILD_OBJECT('name', t.name, 'color', t.color) ORDER BY t.name)::TEXT
				FROM tags t JOIN server_tags st ON st.tag_id = t.id WHERE st.server_id = s.id
			), '[]'),
			COALESCE(s.server_type, 'linux'), COALESCE(s.cpu_cores, 0), COALESCE(s.disk_total_bytes, 0),
			s.expires_at, COALESCE(s.billing_price, 0), COALESCE(s.billing_currency, 'CNY'),
			COALESCE(s.billing_cycle, 'year'), COALESCE(s.traffic_limit_bytes, 0),
			COALESCE(sm.cpu_percent, 0), COALESCE(sm.load_1, 0), COALESCE(sm.load_5, 0), COALESCE(sm.load_15, 0),
			COALESCE(sm.memory_used, 0), COALESCE(sm.memory_total, 0), COALESCE(sm.disk_used_bytes, 0),
			COALESCE(sm.network_rx_bytes, 0), COALESCE(sm.network_tx_bytes, 0),
			COALESCE(sm.network_rx_total_bytes, 0), COALESCE(sm.network_tx_total_bytes, 0),
			COALESCE(sm.uptime_seconds, 0), COALESCE(sm.latency_ms, 0), sm.recorded_at,
			COALESCE((
				SELECT COUNT(*) FROM server_metrics_15m q
				WHERE q.server_id = s.id AND q.recorded_at >= GREATEST($1, s.created_at) AND q.recorded_at < $2
			), 0),
			-- Counts quarter-hour boundaries in the range, mirroring
			-- services.ExpectedBuckets: the rollup floors recorded_at onto those
			-- boundaries, so dividing the raw duration would undercount by one
			-- whenever created_at is not itself aligned, and the count above
			-- would then exceed it.
			GREATEST(0, (CEIL(EXTRACT(EPOCH FROM $2) / 900)
				- CEIL(EXTRACT(EPOCH FROM GREATEST($1, s.created_at)) / 900))::INT)
		FROM servers s
		LEFT JOIN server_latest_metrics sm ON sm.server_id = s.id
		ORDER BY s.created_at ASC, s.id ASC`, since30d, until)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	metrics := make([]PublicServerMetric, 0)
	for rows.Next() {
		var item PublicServerMetric
		var expiresAt, recordedAt sql.NullTime
		var tagsJSON string
		if err := rows.Scan(
			&item.Name, &item.PublicLocation, &tagsJSON,
			&item.ServerType, &item.CPUCores, &item.DiskTotal, &expiresAt,
			&item.BillingPrice, &item.BillingCurrency, &item.BillingCycle, &item.TrafficLimit,
			&item.CPUPercent, &item.Load1, &item.Load5, &item.Load15,
			&item.MemoryUsed, &item.MemoryTotal, &item.DiskUsed,
			&item.NetworkRxBytes, &item.NetworkTxBytes, &item.NetworkRxTotal, &item.NetworkTxTotal,
			&item.UptimeSeconds, &item.LatencyMS, &recordedAt,
			&item.Availability30dObserved, &item.Availability30dExpected,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tagsJSON), &item.Tags); err != nil {
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
