package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type PublicTag struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// PublicServerMetric is deliberately separate from Server. Display names,
// manually configured locations and tags are public; connection settings,
// credentials, ownership and notes are excluded. ServerID is internal join
// state for the independently cached availability census and is never JSON.
type PublicServerMetric struct {
	ServerID          uuid.UUID `json:"-"`
	Name              string
	PublicLocation    string
	Tags              []PublicTag
	ServerType        string
	CPUCores          int
	DiskTotal         int64
	ExpiresAt         *time.Time
	BillingPrice      float64
	BillingCurrency   string
	BillingCycle      string
	TrafficLimit      int64
	CPUPercent        float64
	Load1             float64
	Load5             float64
	Load15            float64
	MemoryUsed        int64
	MemoryTotal       int64
	DiskUsed          int64
	NetworkRxBytes    int64
	NetworkTxBytes    int64
	NetworkRxTotal    int64
	NetworkTxTotal    int64
	UptimeSeconds     int64
	LatencyMS         int
	PacketLossPercent *float64
	RecordedAt        *time.Time
	// Observed 30-day availability, derived from the fifteen-minute rollup.
	// Availability30dObserved/Expected are bucket counts; the handler turns them
	// into a percentage so the arithmetic lives in one place.
	Availability30dObserved int
	Availability30dExpected int
}

// GetPublicServerMetricsContext reads the current public inventory and latest
// samples only. The slower historical census has a separate cache lifetime.
func GetPublicServerMetricsContext(ctx context.Context, db *sql.DB) ([]PublicServerMetric, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT s.id, s.name, COALESCE(s.public_location, ''),
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
			(SELECT (n.sample->>'loss_percent')::float8 FROM network_samples n JOIN network_checks c ON c.id=n.check_id
			 WHERE c.target_server_id=s.id AND c.user_id=s.user_id AND c.source_server_id IS NULL AND c.enabled
			 AND n.recorded_at>NOW()-INTERVAL '2 minutes' ORDER BY n.recorded_at DESC,n.id DESC LIMIT 1)
		FROM servers s
		LEFT JOIN server_latest_metrics sm ON sm.server_id = s.id
		WHERE s.is_public
		ORDER BY s.created_at ASC, s.id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	metrics := make([]PublicServerMetric, 0)
	for rows.Next() {
		var item PublicServerMetric
		var expiresAt, recordedAt sql.NullTime
		var packetLoss sql.NullFloat64
		var tagsJSON string
		if err := rows.Scan(
			&item.ServerID, &item.Name, &item.PublicLocation, &tagsJSON,
			&item.ServerType, &item.CPUCores, &item.DiskTotal, &expiresAt,
			&item.BillingPrice, &item.BillingCurrency, &item.BillingCycle, &item.TrafficLimit,
			&item.CPUPercent, &item.Load1, &item.Load5, &item.Load15,
			&item.MemoryUsed, &item.MemoryTotal, &item.DiskUsed,
			&item.NetworkRxBytes, &item.NetworkTxBytes, &item.NetworkRxTotal, &item.NetworkTxTotal,
			&item.UptimeSeconds, &item.LatencyMS, &recordedAt,
			&packetLoss,
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
		if packetLoss.Valid {
			item.PacketLossPercent = &packetLoss.Float64
		}
		metrics = append(metrics, item)
	}
	return metrics, rows.Err()
}

type PublicAvailability struct {
	Observed int
	Expected int
}

// Recheck visibility in SQL as well as restricting the census to the current
// inventory. Stable IDs join the snapshots; display names need not be unique.
func GetPublicAvailabilityContext(ctx context.Context, db *sql.DB, ids []uuid.UUID, since, until time.Time) (map[uuid.UUID]PublicAvailability, error) {
	result := make(map[uuid.UUID]PublicAvailability, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT s.id,
		(SELECT COUNT(*) FROM server_metrics_15m q WHERE q.server_id=s.id
		 AND q.recorded_at>=GREATEST($1,s.created_at) AND q.recorded_at<$2),
		GREATEST(0,(CEIL(EXTRACT(EPOCH FROM $2::timestamptz)/900)
		 - CEIL(EXTRACT(EPOCH FROM GREATEST($1::timestamptz,s.created_at))/900))::INT)
		FROM servers s WHERE s.is_public AND s.id=ANY($3)`, since, until, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var availability PublicAvailability
		if err := rows.Scan(&id, &availability.Observed, &availability.Expected); err != nil {
			return nil, err
		}
		result[id] = availability
	}
	return result, rows.Err()
}
