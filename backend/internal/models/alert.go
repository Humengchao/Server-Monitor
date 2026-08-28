package models

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
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
	ID         uuid.UUID  `json:"id"`
	UserID     uuid.UUID  `json:"user_id"`
	Name       string     `json:"name"`
	ServerID   *uuid.UUID `json:"server_id"`
	ServerName string     `json:"server_name,omitempty"`
	Metric     string     `json:"metric"`
	Comparator string     `json:"comparator"`
	Threshold  float64    `json:"threshold"`
	Duration   int        `json:"duration_seconds"`
	Enabled    bool       `json:"enabled"`
	WebhookURL string     `json:"webhook_url"`
	CreatedAt  time.Time  `json:"created_at"`
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
	return db.QueryRow(
		`INSERT INTO alert_rules (id, user_id, name, server_id, metric, comparator, threshold, duration_seconds, enabled, webhook_url)
		 SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		 WHERE $4::uuid IS NULL OR EXISTS (SELECT 1 FROM servers WHERE id = $4 AND user_id = $2)
		 RETURNING created_at`,
		r.ID, r.UserID, r.Name, r.ServerID, r.Metric, r.Comparator, r.Threshold, r.Duration, r.Enabled, r.WebhookURL,
	).Scan(&r.CreatedAt)
}

func UpdateAlertRule(db *sql.DB, r *AlertRule) error {
	result, err := db.Exec(
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
	return nil
}

func DeleteAlertRule(db *sql.DB, id, userID uuid.UUID) error {
	_, err := db.Exec(`DELETE FROM alert_rules WHERE id=$1 AND user_id=$2`, id, userID)
	return err
}

func GetAlertRulesByUserID(db *sql.DB, userID uuid.UUID) ([]AlertRule, error) {
	rows, err := db.Query(
		`SELECT r.id, r.user_id, r.name, r.server_id, COALESCE(s.name, ''), r.metric, r.comparator,
		 r.threshold, r.duration_seconds, r.enabled, r.webhook_url, r.created_at,
		 (SELECT COUNT(*) FROM alert_events e WHERE e.rule_id = r.id AND e.resolved_at IS NULL)
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
		 r.threshold, r.duration_seconds, r.enabled, r.webhook_url, r.created_at, 0
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
		if err := rows.Scan(&r.ID, &r.UserID, &r.Name, &serverID, &r.ServerName, &r.Metric, &r.Comparator,
			&r.Threshold, &r.Duration, &r.Enabled, &r.WebhookURL, &r.CreatedAt, &r.FiringCount); err != nil {
			return nil, err
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
		 LEFT JOIN server_latest_metrics lm ON lm.server_id = s.id`)
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

// PruneAlertEvents drops resolved events older than the retention window so the
// history table stays bounded.
func PruneAlertEvents(db *sql.DB, before time.Time) error {
	_, err := db.Exec(`DELETE FROM alert_events WHERE resolved_at IS NOT NULL AND resolved_at < $1`, before)
	return err
}
