package models

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

var ErrAIPanelAlertRevision = errors.New("alert rule or notification channels changed; read the current configuration and submit a new operation")
var ErrAIPanelAlertScope = errors.New("alert target is outside this task's selected server scope")
var ErrAIPanelAlertExists = errors.New("an alert with this name and target already exists with different settings; read it and use monitor_update_alert")
var ErrAIPanelAlertCommit = errors.New("alert mutation commit outcome is unknown")

// AIPanelAlertArguments contains only model-visible rule fields. Webhook URLs,
// channel destinations and account identifiers are never accepted or returned.
type AIPanelAlertArguments struct {
	RuleID           uuid.UUID   `json:"rule_id,omitempty"`
	Name             string      `json:"name,omitempty"`
	Scope            string      `json:"scope,omitempty"`
	ServerID         *uuid.UUID  `json:"server_id,omitempty"`
	Metric           string      `json:"metric,omitempty"`
	Comparator       string      `json:"comparator,omitempty"`
	Threshold        float64     `json:"threshold"`
	Duration         int         `json:"duration_seconds,omitempty"`
	Enabled          bool        `json:"enabled"`
	ChannelIDs       []uuid.UUID `json:"channel_ids"`
	ExpectedRevision string      `json:"expected_revision"`
}

type AIPanelAlertResult struct {
	RuleID        uuid.UUID `json:"rule_id"`
	Operation     string    `json:"operation"`
	AlreadyExists bool      `json:"already_exists,omitempty"`
	Message       string    `json:"message"`
}

// NormalizeAIPanelAlert mirrors the alert editor's metric/range rules. The AI
// tools additionally require an explicit scope and reject non-finite values.
func NormalizeAIPanelAlert(a *AIPanelAlertArguments) error {
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" || len(a.Name) > 128 || !utf8.ValidString(a.Name) || strings.ContainsRune(a.Name, 0) {
		return errors.New("rule name must be UTF-8 text of 1 to 128 bytes")
	}
	if a.Scope != "server" && a.Scope != "all_owned" {
		return errors.New("scope must be server or all_owned")
	}
	if a.Scope == "server" && (a.ServerID == nil || *a.ServerID == uuid.Nil) || a.Scope == "all_owned" && a.ServerID != nil {
		return errors.New("server scope requires server_id; all_owned must not include server_id")
	}
	a.Metric = strings.ToLower(strings.TrimSpace(a.Metric))
	if !ValidAlertMetric(a.Metric) {
		return errors.New("unsupported alert metric")
	}
	a.Comparator = strings.TrimSpace(a.Comparator)
	if a.Comparator == "" {
		a.Comparator = ">"
	}
	if a.Comparator != ">" && a.Comparator != "<" {
		return errors.New("comparator must be > or <")
	}
	if math.IsNaN(a.Threshold) || math.IsInf(a.Threshold, 0) || a.Threshold < 0 {
		return errors.New("threshold must be finite and non-negative")
	}
	switch a.Metric {
	case AlertMetricCPU, AlertMetricMemory, AlertMetricDisk:
		if a.Threshold > 100 {
			return errors.New("percentage threshold must be between 0 and 100")
		}
	case AlertMetricOffline:
		a.Threshold, a.Comparator = 0, ">"
	}
	if a.Duration == 0 {
		a.Duration = 300
	}
	if a.Duration < 30 || a.Duration > 86400 {
		return errors.New("duration must be between 30 and 86400 seconds")
	}
	if len(a.ChannelIDs) > 32 {
		return errors.New("at most 32 notification channels per rule")
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range a.ChannelIDs {
		if id == uuid.Nil || seen[id] {
			return errors.New("notification channel IDs must be nonempty and unique")
		}
		seen[id] = true
	}
	sort.Slice(a.ChannelIDs, func(i, j int) bool { return a.ChannelIDs[i].String() < a.ChannelIDs[j].String() })
	return nil
}

type aiPanelAlertQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func aiPanelAlertRule(ctx context.Context, q aiPanelAlertQuery, user, id uuid.UUID, lock bool) (AlertRule, error) {
	var r AlertRule
	var server uuid.NullUUID
	query := `SELECT id,user_id,name,server_id,metric,comparator,threshold,duration_seconds,enabled,webhook_url FROM alert_rules WHERE id=$1 AND user_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	err := q.QueryRowContext(ctx, query, id, user).Scan(&r.ID, &r.UserID, &r.Name, &server, &r.Metric, &r.Comparator, &r.Threshold, &r.Duration, &r.Enabled, &r.WebhookURL)
	if err != nil {
		return r, err
	}
	if server.Valid {
		r.ServerID = &server.UUID
	}
	r.ChannelIDs = []uuid.UUID{}
	rows, err := q.QueryContext(ctx, `SELECT channel_id FROM alert_rule_channels WHERE rule_id=$1 ORDER BY channel_id`, id)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			return r, err
		}
		r.ChannelIDs = append(r.ChannelIDs, id)
	}
	return r, rows.Err()
}

func aiPanelAlertRevision(ctx context.Context, q aiPanelAlertQuery, user uuid.UUID, r *AlertRule, channelIDs []uuid.UUID, lock bool) (string, error) {
	type channelRevision struct {
		ID       uuid.UUID
		Revision int64
		Enabled  bool
	}
	channels := []channelRevision{}
	for _, id := range channelIDs {
		c := channelRevision{ID: id}
		query := `SELECT revision,enabled FROM notification_channels WHERE id=$1 AND user_id=$2`
		if lock {
			query += ` FOR SHARE`
		}
		if err := q.QueryRowContext(ctx, query, id, user).Scan(&c.Revision, &c.Enabled); err != nil {
			if err == sql.ErrNoRows {
				return "", ErrNotificationChannels
			}
			return "", err
		}
		channels = append(channels, c)
	}
	// AlertRule includes the webhook in the digest only, never its public output.
	raw, err := json.Marshal([]any{r, channels})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func aiPanelAlertCheckScope(ctx context.Context, q aiPanelAlertQuery, user, selected uuid.UUID, server *uuid.UUID, lock bool) error {
	if selected != uuid.Nil && (server == nil || *server != selected) {
		return ErrAIPanelAlertScope
	}
	if server != nil {
		query := `SELECT id FROM servers WHERE id=$1 AND user_id=$2`
		if lock {
			query += ` FOR SHARE`
		}
		var owned uuid.UUID
		return q.QueryRowContext(ctx, query, *server, user).Scan(&owned)
	}
	return nil
}

// PrepareAIPanelAlert binds the exact rule and notification channel revisions.
// Passing a saved revision validates it; it is never silently refreshed.
func PrepareAIPanelAlert(ctx context.Context, db *sql.DB, user, selected uuid.UUID, operation string, a *AIPanelAlertArguments) error {
	var current *AlertRule
	if operation != "monitor_create_alert" {
		r, err := aiPanelAlertRule(ctx, db, user, a.RuleID, false)
		if err != nil {
			return err
		}
		if err = aiPanelAlertCheckScope(ctx, db, user, selected, r.ServerID, false); err != nil {
			return err
		}
		current = &r
		if a.ChannelIDs == nil {
			a.ChannelIDs = append([]uuid.UUID{}, r.ChannelIDs...)
		}
	}
	if operation != "monitor_delete_alert" {
		if err := NormalizeAIPanelAlert(a); err != nil {
			return err
		}
		if err := aiPanelAlertCheckScope(ctx, db, user, selected, a.ServerID, false); err != nil {
			return err
		}
	}
	if a.ChannelIDs == nil {
		a.ChannelIDs = []uuid.UUID{}
	}
	revision, err := aiPanelAlertRevision(ctx, db, user, current, a.ChannelIDs, false)
	if err != nil {
		return err
	}
	if a.ExpectedRevision != "" && a.ExpectedRevision != revision {
		return ErrAIPanelAlertRevision
	}
	a.ExpectedRevision = revision
	return nil
}

// ApplyAIPanelAlert serializes authority checks, the configuration write and
// its action-ID receipt. It can never issue a notification or contact a host.
func ApplyAIPanelAlert(ctx context.Context, db *sql.DB, task, user, action uuid.UUID, worker, operation string, arguments json.RawMessage) (AIPanelAlertResult, error) {
	var result AIPanelAlertResult
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var selected uuid.UUID
	var mode string
	if err = tx.QueryRowContext(ctx, `SELECT server_id,approval_mode FROM ai_tasks WHERE id=$1 AND user_id=$2 AND worker_id=$3 AND status='running' AND lease_until>NOW() FOR UPDATE`, task, user, worker).Scan(&selected, &mode); err != nil {
		return result, err
	}
	if mode != "standard" && mode != "automatic" {
		return result, errors.New("read-only tasks cannot change alert rules")
	}
	var approved bool
	if err = tx.QueryRowContext(ctx, `SELECT NOT requires_approval OR approved_at IS NOT NULL FROM ai_actions WHERE id=$1 AND task_id=$2 AND status='running' AND tool_name=$3 AND arguments=$4::jsonb AND mutating FOR UPDATE`, action, task, operation, []byte(arguments)).Scan(&approved); err != nil {
		return result, err
	}
	if !approved {
		return result, ErrAITaskConflict
	}
	var receipt []byte
	err = tx.QueryRowContext(ctx, `SELECT result FROM ai_panel_alert_mutations WHERE action_id=$1`, action).Scan(&receipt)
	if err == nil {
		if err = json.Unmarshal(receipt, &result); err != nil {
			return result, err
		}
		return result, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return result, err
	}
	var a AIPanelAlertArguments
	if err = json.Unmarshal(arguments, &a); err != nil {
		return result, err
	}
	if operation != "monitor_create_alert" && operation != "monitor_update_alert" && operation != "monitor_delete_alert" {
		return result, errors.New("unknown panel mutation")
	}
	if mode == "standard" && (operation != "monitor_create_alert" || !a.Enabled) {
		var exactApproval bool
		if err = tx.QueryRowContext(ctx, `SELECT requires_approval AND approved_at IS NOT NULL FROM ai_actions WHERE id=$1`, action).Scan(&exactApproval); err != nil {
			return result, err
		}
		if !exactApproval {
			return result, ErrAITaskConflict
		}
	}
	// Lock AI mutations for one account before duplicate detection. Existing UI
	// updates also serialize on each rule row acquired below.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "ai-panel-alert/"+user.String()); err != nil {
		return result, err
	}
	var current *AlertRule
	if operation != "monitor_create_alert" {
		r, readErr := aiPanelAlertRule(ctx, tx, user, a.RuleID, true)
		if readErr != nil {
			return result, readErr
		}
		if err = aiPanelAlertCheckScope(ctx, tx, user, selected, r.ServerID, true); err != nil {
			return result, err
		}
		current = &r
	}
	if operation != "monitor_delete_alert" {
		if err = NormalizeAIPanelAlert(&a); err != nil {
			return result, err
		}
		if err = aiPanelAlertCheckScope(ctx, tx, user, selected, a.ServerID, true); err != nil {
			return result, err
		}
	}
	revision, err := aiPanelAlertRevision(ctx, tx, user, current, a.ChannelIDs, true)
	if err != nil {
		return result, err
	}
	if revision != a.ExpectedRevision {
		return result, ErrAIPanelAlertRevision
	}
	result.Operation = operation
	switch operation {
	case "monitor_create_alert":
		var id uuid.UUID
		err = tx.QueryRowContext(ctx, `SELECT id FROM alert_rules WHERE user_id=$1 AND name=$2 AND server_id IS NOT DISTINCT FROM $3::uuid ORDER BY created_at,id LIMIT 1 FOR UPDATE`, user, a.Name, a.ServerID).Scan(&id)
		if err == nil {
			existing, readErr := aiPanelAlertRule(ctx, tx, user, id, false)
			if readErr != nil {
				return result, readErr
			}
			if !aiPanelAlertSame(existing, a) {
				return result, ErrAIPanelAlertExists
			}
			result.RuleID, result.AlreadyExists, result.Message = id, true, "An identical alert rule already exists; no duplicate was created."
		} else if err == sql.ErrNoRows {
			result.RuleID = uuid.New()
			_, err = tx.ExecContext(ctx, `INSERT INTO alert_rules(id,user_id,name,server_id,metric,comparator,threshold,duration_seconds,enabled) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, result.RuleID, user, a.Name, a.ServerID, a.Metric, a.Comparator, a.Threshold, a.Duration, a.Enabled)
			if err != nil {
				return result, err
			}
			if err = setAlertRuleChannels(ctx, tx, result.RuleID, user, a.ChannelIDs); err != nil {
				return result, err
			}
			result.Message = "Alert rule created in Server Monitor."
		} else {
			return result, err
		}
	case "monitor_update_alert":
		result.RuleID = a.RuleID
		_, err = tx.ExecContext(ctx, `UPDATE alert_rules SET name=$1,server_id=$2,metric=$3,comparator=$4,threshold=$5,duration_seconds=$6,enabled=$7 WHERE id=$8 AND user_id=$9`, a.Name, a.ServerID, a.Metric, a.Comparator, a.Threshold, a.Duration, a.Enabled, a.RuleID, user)
		if err != nil {
			return result, err
		}
		if err = setAlertRuleChannels(ctx, tx, a.RuleID, user, a.ChannelIDs); err != nil {
			return result, err
		}
		if !a.Enabled {
			if _, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,last_error='Rule disabled' WHERE rule_id=$1 AND user_id=$2 AND status IN ('pending','retry','sending')`, a.RuleID, user); err != nil {
				return result, err
			}
		}
		// Keep the legacy webhook unchanged. Existing queued webhook attempts are
		// removed under the same conditions as an ordinary alert editor update.
		if !a.Enabled || current.WebhookURL == "" || len(a.ChannelIDs) > 0 {
			if _, err = tx.ExecContext(ctx, `DELETE FROM alert_webhooks WHERE rule_id=$1 AND user_id=$2 AND delivered_at IS NULL`, a.RuleID, user); err != nil {
				return result, err
			}
		}
		result.Message = "Alert rule updated in Server Monitor; its existing webhook was preserved."
	case "monitor_delete_alert":
		result.RuleID = a.RuleID
		if _, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,last_error='Rule deleted' WHERE rule_id=$1 AND user_id=$2 AND status IN ('pending','retry','sending')`, a.RuleID, user); err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM alert_rules WHERE id=$1 AND user_id=$2`, a.RuleID, user); err != nil {
			return result, err
		}
		result.Message = "Alert rule deleted from Server Monitor."
	}
	// clock_timestamp(), unlike transaction NOW(), detects a lease expiring
	// while a contended rule/channel row was being acquired.
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT lease_until>clock_timestamp() FROM ai_tasks WHERE id=$1`, task).Scan(&active); err != nil {
		return result, err
	}
	if !active {
		return result, ErrAITaskConflict
	}
	receipt, err = json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_panel_alert_mutations(action_id,result) VALUES($1,$2)`, action, receipt); err != nil {
		return result, err
	}
	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("%w: %v", ErrAIPanelAlertCommit, err)
	}
	return result, nil
}

func aiPanelAlertSame(r AlertRule, a AIPanelAlertArguments) bool {
	if r.Metric != a.Metric || r.Comparator != a.Comparator || r.Threshold != a.Threshold || r.Duration != a.Duration || r.Enabled != a.Enabled || len(r.ChannelIDs) != len(a.ChannelIDs) {
		return false
	}
	for i := range r.ChannelIDs {
		if r.ChannelIDs[i] != a.ChannelIDs[i] {
			return false
		}
	}
	return true
}

// Keep compile-time checks close to the query helpers used for both optimistic
// reads and locked writes.
var _ aiPanelAlertQuery = (*sql.DB)(nil)
var _ aiPanelAlertQuery = (*sql.Tx)(nil)
