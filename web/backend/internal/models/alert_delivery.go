package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/google/uuid"
	"time"
)

// AlertNotification commits together with its event transition.
type AlertNotification struct {
	URL        string
	Payload    json.RawMessage
	Transition string
}

func enqueueAlertNotification(ctx context.Context, tx *sql.Tx, eventID int64, userID, ruleID, serverID uuid.UUID, notification *AlertNotification) error {
	if notification == nil {
		return nil
	}
	var target string
	var enabled bool
	// Serialize with rule edits so an evaluation that started before an edit
	// cannot enqueue the old URL after the edit already retargeted the queue.
	if err := tx.QueryRowContext(ctx, `SELECT webhook_url,enabled FROM alert_rules WHERE id=$1 AND user_id=$2 FOR SHARE`, ruleID, userID).Scan(&target, &enabled); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if associated, err := enqueueChannelNotifications(ctx, tx, eventID, userID, ruleID, serverID, notification); err != nil || associated {
		return err
	}
	if target == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO alert_webhooks(event_id,user_id,rule_id,server_id,transition,target,payload)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(event_id,transition) DO NOTHING`,
		eventID, userID, ruleID, serverID, notification.Transition, target, string(notification.Payload))
	return err
}

func OpenAlertEventWithNotification(ctx context.Context, db *sql.DB, event *AlertEvent, notification *AlertNotification) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO alert_events(rule_id,server_id,user_id,value,message,started_at)
		VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(rule_id,server_id) WHERE resolved_at IS NULL DO NOTHING RETURNING id`,
		event.RuleID, event.ServerID, event.UserID, event.Value, event.Message, event.StartedAt).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err = enqueueAlertNotification(ctx, tx, id, event.UserID, event.RuleID, event.ServerID, notification); err != nil {
		return 0, err
	}
	return id, commitNotificationWork(tx, db)
}

func ResolveAlertEventWithNotification(ctx context.Context, db *sql.DB, ruleID, serverID uuid.UUID, at time.Time, notification *AlertNotification) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var id int64
	var userID uuid.UUID
	err = tx.QueryRowContext(ctx, `UPDATE alert_events SET resolved_at=$1
		WHERE rule_id=$2 AND server_id=$3 AND resolved_at IS NULL RETURNING id,user_id`, at, ruleID, serverID).Scan(&id, &userID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = enqueueAlertNotification(ctx, tx, id, userID, ruleID, serverID, notification); err != nil {
		return false, err
	}
	return true, commitNotificationWork(tx, db)
}

type PendingWebhook struct {
	ID       int64
	Target   string
	Payload  json.RawMessage
	Attempts int
}

// Leases recover after a crash; each rule/server pair is delivered in order.
func ClaimAlertWebhooks(ctx context.Context, db *sql.DB, limit int) ([]PendingWebhook, error) {
	rows, err := db.QueryContext(ctx, `UPDATE alert_webhooks SET attempts=attempts+1,next_attempt_at=NOW()+INTERVAL '1 minute'
		WHERE id IN (SELECT w.id FROM alert_webhooks w WHERE w.delivered_at IS NULL AND w.next_attempt_at<=NOW()
		AND NOT EXISTS(SELECT 1 FROM alert_webhooks earlier WHERE earlier.rule_id=w.rule_id AND earlier.server_id=w.server_id
		 AND earlier.delivered_at IS NULL AND earlier.id<w.id)
		ORDER BY w.id LIMIT $1 FOR UPDATE OF w SKIP LOCKED)
		RETURNING id,target,payload,attempts`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []PendingWebhook
	for rows.Next() {
		var item PendingWebhook
		if err := rows.Scan(&item.ID, &item.Target, &item.Payload, &item.Attempts); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func FinishAlertWebhook(ctx context.Context, db *sql.DB, item PendingWebhook, delivered bool, retryAfter time.Duration) error {
	if delivered {
		_, err := db.ExecContext(ctx, `UPDATE alert_webhooks SET delivered_at=NOW()
			WHERE id=$1 AND target=$2 AND attempts=$3 AND delivered_at IS NULL`, item.ID, item.Target, item.Attempts)
		return err
	}
	_, err := db.ExecContext(ctx, `UPDATE alert_webhooks SET next_attempt_at=NOW()+($4*INTERVAL '1 second')
		WHERE id=$1 AND target=$2 AND attempts=$3 AND delivered_at IS NULL`, item.ID, item.Target, item.Attempts, int(retryAfter.Seconds()))
	return err
}
