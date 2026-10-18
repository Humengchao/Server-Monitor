package models

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Alert metrics the engine knows how to sample. Percent-based metrics are
// evaluated against 0-100 so a rule reads the same way as the dashboard.
const (
	AlertMetricCPU     = "cpu"
	AlertMetricMemory  = "memory"
	AlertMetricDisk    = "disk"
	AlertMetricLoad1   = "load1"
	AlertMetricLatency = "latency"
	AlertMetricOffline = "offline"
)

// AlertMetricUnits maps each metric to the unit used when rendering messages.
var AlertMetricUnits = map[string]string{
	AlertMetricCPU:     "%",
	AlertMetricMemory:  "%",
	AlertMetricDisk:    "%",
	AlertMetricLoad1:   "",
	AlertMetricLatency: "ms",
	AlertMetricOffline: "",
}

// ValidAlertMetric reports whether metric is one the engine can evaluate.
func ValidAlertMetric(metric string) bool {
	_, ok := AlertMetricUnits[metric]
	return ok
}

type AlertRule struct {
	ID         uuid.UUID   `json:"id"`
	UserID     uuid.UUID   `json:"user_id"`
	Name       string      `json:"name"`
	ServerID   *uuid.UUID  `json:"server_id"`
	ServerName string      `json:"server_name,omitempty"`
	Metric     string      `json:"metric"`
	Comparator string      `json:"comparator"`
	Threshold  float64     `json:"threshold"`
	Duration   int         `json:"duration_seconds"`
	Enabled    bool        `json:"enabled"`
	WebhookURL string      `json:"webhook_url"`
	ChannelIDs []uuid.UUID `json:"channel_ids"`
	CreatedAt  time.Time   `json:"created_at"`
	// FiringCount is the number of servers currently breaching this rule. It is
	// filled in by list queries so the UI can badge a rule without a second call.
	FiringCount int `json:"firing_count"`
}

type AlertEvent struct {
	ID         int64     `json:"id"`
	RuleID     uuid.UUID `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	Metric     string    `json:"metric"`
	ServerID   uuid.UUID `json:"server_id"`
	ServerName string    `json:"server_name"`
	UserID     uuid.UUID `json:"-"`
	Value      float64   `json:"value"`
	// Message is the English summary recorded when the event opened. The UI
	// prefers to re-render it from the fields below so the text follows the
	// selected language; this stays as the fallback for deleted rules.
	Message    string     `json:"message"`
	Comparator string     `json:"comparator"`
	Threshold  float64    `json:"threshold"`
	Duration   int        `json:"duration_seconds"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

// AlertSnapshot is one server's current state as seen by the alert engine.
// It intentionally carries no connection details or secrets.
type AlertSnapshot struct {
	ServerID   uuid.UUID
	UserID     uuid.UUID
	ServerName string
	DiskTotal  int64
	HasMetrics bool
	CPUPercent float64
	MemoryUsed int64
	MemTotal   int64
	DiskUsed   int64
	Load1      float64
	LatencyMS  int
	RecordedAt time.Time
}

func CreateAlertRule(db *sql.DB, r *AlertRule) error {
	r.ID = uuid.New()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.QueryRow(
		`INSERT INTO alert_rules (id, user_id, name, server_id, metric, comparator, threshold, duration_seconds, enabled, webhook_url)
		 SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		 WHERE $4::uuid IS NULL OR EXISTS (SELECT 1 FROM servers WHERE id = $4 AND user_id = $2)
		 RETURNING created_at`,
		r.ID, r.UserID, r.Name, r.ServerID, r.Metric, r.Comparator, r.Threshold, r.Duration, r.Enabled, r.WebhookURL,
	).Scan(&r.CreatedAt)
	if err != nil {
		return err
	}
	if err = setAlertRuleChannels(context.Background(), tx, r.ID, r.UserID, r.ChannelIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func UpdateAlertRule(db *sql.DB, r *AlertRule) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(
		`UPDATE alert_rules SET name=$1, server_id=$2, metric=$3, comparator=$4, threshold=$5,
		 duration_seconds=$6, enabled=$7, webhook_url=$8
		 WHERE id=$9 AND user_id=$10
		 AND ($2::uuid IS NULL OR EXISTS (SELECT 1 FROM servers WHERE id = $2 AND user_id = $10))`,
		r.Name, r.ServerID, r.Metric, r.Comparator, r.Threshold, r.Duration, r.Enabled, r.WebhookURL, r.ID, r.UserID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	if err = setAlertRuleChannels(context.Background(), tx, r.ID, r.UserID, r.ChannelIDs); err != nil {
		return err
	}
	if !r.Enabled {
		if _, err = tx.Exec(`UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,last_error='Rule disabled'
		 WHERE rule_id=$1 AND user_id=$2 AND status IN ('pending','retry','sending')`, r.ID, r.UserID); err != nil {
			return err
		}
	}
	var hasChannels bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM alert_rule_channels WHERE rule_id=$1)`, r.ID).Scan(&hasChannels); err != nil {
		return err
	}
	// Fixing a webhook address must also unblock already queued deliveries.
	// Disabled notifications are canceled; an in-flight HTTP request cannot be
	// retracted, but its late acknowledgement cannot complete a retargeted row.
	if !r.Enabled || r.WebhookURL == "" || hasChannels {
		_, err = tx.Exec(`DELETE FROM alert_webhooks WHERE rule_id=$1 AND user_id=$2 AND delivered_at IS NULL`, r.ID, r.UserID)
	} else {
		_, err = tx.Exec(`UPDATE alert_webhooks SET target=$1,attempts=0,next_attempt_at=NOW()
			WHERE rule_id=$2 AND user_id=$3 AND delivered_at IS NULL AND target<>$1`, r.WebhookURL, r.ID, r.UserID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func DeleteAlertRule(db *sql.DB, id, userID uuid.UUID) error {
	_, err := db.Exec(`DELETE FROM alert_rules WHERE id=$1 AND user_id=$2`, id, userID)
	return err
}

func GetAlertRulesByUserID(db *sql.DB, userID uuid.UUID) ([]AlertRule, error) {
	rows, err := db.Query(
		`SELECT r.id, r.user_id, r.name, r.server_id, COALESCE(s.name, ''), r.metric, r.comparator,
		 r.threshold, r.duration_seconds, r.enabled, r.webhook_url, r.created_at,
		 (SELECT COUNT(*) FROM alert_events e WHERE e.rule_id = r.id AND e.resolved_at IS NULL),
		 COALESCE((SELECT ARRAY_AGG(rc.channel_id ORDER BY rc.channel_id) FROM alert_rule_channels rc WHERE rc.rule_id=r.id),'{}'::uuid[])
		 FROM alert_rules r
		 LEFT JOIN servers s ON s.id = r.server_id
		 WHERE r.user_id = $1
		 ORDER BY r.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlertRules(rows)
}

// GetEnabledAlertRules returns every enabled rule across all users for the
// evaluation loop.
func GetEnabledAlertRules(db *sql.DB) ([]AlertRule, error) {
	rows, err := db.Query(
		`SELECT r.id, r.user_id, r.name, r.server_id, '', r.metric, r.comparator,
		 r.threshold, r.duration_seconds, r.enabled, r.webhook_url, r.created_at, 0,
		 COALESCE((SELECT ARRAY_AGG(rc.channel_id ORDER BY rc.channel_id) FROM alert_rule_channels rc WHERE rc.rule_id=r.id),'{}'::uuid[])
		 FROM alert_rules r WHERE r.enabled ORDER BY r.created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlertRules(rows)
}

func scanAlertRules(rows *sql.Rows) ([]AlertRule, error) {
	var rules []AlertRule
	for rows.Next() {
		var r AlertRule
		var serverID sql.NullString
		var channelIDs pq.StringArray
		if err := rows.Scan(&r.ID, &r.UserID, &r.Name, &serverID, &r.ServerName, &r.Metric, &r.Comparator,
			&r.Threshold, &r.Duration, &r.Enabled, &r.WebhookURL, &r.CreatedAt, &r.FiringCount, &channelIDs); err != nil {
			return nil, err
		}
		r.ChannelIDs = []uuid.UUID{}
		for _, raw := range channelIDs {
			id, err := uuid.Parse(raw)
			if err != nil {
				return nil, err
			}
			r.ChannelIDs = append(r.ChannelIDs, id)
		}
		if serverID.Valid {
			if id, err := uuid.Parse(serverID.String); err == nil {
				r.ServerID = &id
			}
		}
		rules = append(rules, r)
	}
	return rules, rows.Err()
}

// GetAlertSnapshots returns the current state of every monitored server, used
// by the engine to evaluate rules without one query per server.
func GetAlertSnapshots(db *sql.DB) ([]AlertSnapshot, error) {
	rows, err := db.Query(
		`SELECT s.id, s.user_id, s.name, COALESCE(s.disk_total_bytes, 0),
		 lm.server_id IS NOT NULL,
		 COALESCE(lm.cpu_percent, 0), COALESCE(lm.memory_used, 0), COALESCE(lm.memory_total, 0),
		 COALESCE(lm.disk_used_bytes, 0), COALESCE(lm.load_1, 0), COALESCE(lm.latency_ms, 0),
		 COALESCE(lm.recorded_at, TIMESTAMPTZ 'epoch')
		 FROM servers s
		 LEFT JOIN server_latest_metrics lm ON lm.server_id = s.id WHERE s.monitoring_enabled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var snapshots []AlertSnapshot
	for rows.Next() {
		var s AlertSnapshot
		if err := rows.Scan(&s.ServerID, &s.UserID, &s.ServerName, &s.DiskTotal, &s.HasMetrics,
			&s.CPUPercent, &s.MemoryUsed, &s.MemTotal, &s.DiskUsed, &s.Load1, &s.LatencyMS, &s.RecordedAt); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, rows.Err()
}

// OpenAlertEvent inserts a firing event, or returns 0 when one is already open
// for this rule/server pair. The partial unique index makes this race-free.
func OpenAlertEvent(db *sql.DB, e *AlertEvent) (int64, error) {
	var id int64
	err := db.QueryRow(
		`INSERT INTO alert_events (rule_id, server_id, user_id, value, message, started_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (rule_id, server_id) WHERE resolved_at IS NULL DO NOTHING
		 RETURNING id`,
		e.RuleID, e.ServerID, e.UserID, e.Value, e.Message, e.StartedAt).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return id, err
}

// ResolveAlertEvent closes the open event for a rule/server pair and reports
// whether one was actually open.
func ResolveAlertEvent(db *sql.DB, ruleID, serverID uuid.UUID, at time.Time) (bool, error) {
	result, err := db.Exec(
		`UPDATE alert_events SET resolved_at=$1 WHERE rule_id=$2 AND server_id=$3 AND resolved_at IS NULL`,
		at, ruleID, serverID)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

// GetOpenAlertKeys returns "ruleID/serverID" pairs that are currently firing,
// so the engine can rebuild its in-memory view after a restart.
func GetOpenAlertKeys(db *sql.DB) (map[uuid.UUID]map[uuid.UUID]struct{}, error) {
	rows, err := db.Query(`SELECT rule_id, server_id FROM alert_events WHERE resolved_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	open := make(map[uuid.UUID]map[uuid.UUID]struct{})
	for rows.Next() {
		var ruleID, serverID uuid.UUID
		if err := rows.Scan(&ruleID, &serverID); err != nil {
			return nil, err
		}
		if open[ruleID] == nil {
			open[ruleID] = make(map[uuid.UUID]struct{})
		}
		open[ruleID][serverID] = struct{}{}
	}
	return open, rows.Err()
}

func GetAlertEventsByUserID(db *sql.DB, userID uuid.UUID, activeOnly bool, limit int) ([]AlertEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	filter := ""
	if activeOnly {
		filter = " AND e.resolved_at IS NULL"
	}
	rows, err := db.Query(fmt.Sprintf(
		`SELECT e.id, e.rule_id, COALESCE(r.name, ''), COALESCE(r.metric, ''), e.server_id, COALESCE(s.name, ''),
		 e.value, e.message, COALESCE(r.comparator, '>'), COALESCE(r.threshold, 0), COALESCE(r.duration_seconds, 0),
		 e.started_at, e.resolved_at
		 FROM alert_events e
		 LEFT JOIN alert_rules r ON r.id = e.rule_id
		 LEFT JOIN servers s ON s.id = e.server_id
		 WHERE e.user_id = $1%s
		 ORDER BY e.resolved_at IS NULL DESC, e.started_at DESC
		 LIMIT $2`, filter), userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []AlertEvent
	for rows.Next() {
		var e AlertEvent
		var resolvedAt sql.NullTime
		if err := rows.Scan(&e.ID, &e.RuleID, &e.RuleName, &e.Metric, &e.ServerID, &e.ServerName,
			&e.Value, &e.Message, &e.Comparator, &e.Threshold, &e.Duration,
			&e.StartedAt, &resolvedAt); err != nil {
			return nil, err
		}
		if resolvedAt.Valid {
			e.ResolvedAt = &resolvedAt.Time
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// CountActiveAlerts powers the header badge.
func CountActiveAlerts(db *sql.DB, userID uuid.UUID) (int, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM alert_events WHERE user_id=$1 AND resolved_at IS NULL`, userID).Scan(&count)
	return count, err
}

// ServerAlertSummary includes every active event on a host, independently of
// the event history's page size. Rule names provide the dashboard's tooltip.
type ServerAlertSummary struct {
	ServerID  uuid.UUID `json:"server_id"`
	Active    int       `json:"active"`
	RuleNames []string  `json:"rule_names"`
}

type ActiveAlertSummary struct {
	Active  int                  `json:"active"`
	Servers []ServerAlertSummary `json:"servers"`
}

func GetActiveAlertSummary(ctx context.Context, db *sql.DB, userID uuid.UUID) (*ActiveAlertSummary, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.server_id, COUNT(*),
		 COALESCE(ARRAY_AGG(r.name ORDER BY e.started_at DESC, e.id DESC)
		   FILTER (WHERE COALESCE(r.name, '') <> ''), '{}'::text[])
		 FROM alert_events e
		 LEFT JOIN alert_rules r ON r.id = e.rule_id AND r.user_id = e.user_id
		 WHERE e.user_id = $1 AND e.resolved_at IS NULL
		 GROUP BY e.server_id
		 ORDER BY e.server_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summary := &ActiveAlertSummary{Servers: []ServerAlertSummary{}}
	for rows.Next() {
		var server ServerAlertSummary
		if err := rows.Scan(&server.ServerID, &server.Active, pq.Array(&server.RuleNames)); err != nil {
			return nil, err
		}
		summary.Active += server.Active
		summary.Servers = append(summary.Servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return summary, nil
}

// PruneAlertEvents drops resolved events older than the retention window so the
// history table stays bounded.
func PruneAlertEvents(db *sql.DB, before time.Time) error {
	_, err := db.Exec(`DELETE FROM alert_events WHERE resolved_at IS NOT NULL AND resolved_at < $1`, before)
	return err
}
