package models

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
)

type NotificationDelivery struct {
	ID            int64      `json:"id"`
	ChannelID     *uuid.UUID `json:"channel_id"`
	ChannelName   string     `json:"channel_name"`
	ChannelType   string     `json:"channel_type"`
	EventID       *int64     `json:"event_id,omitempty"`
	RuleID        *uuid.UUID `json:"rule_id,omitempty"`
	ServerID      *uuid.UUID `json:"server_id,omitempty"`
	Transition    string     `json:"transition"`
	Subject       string     `json:"subject"`
	Message       string     `json:"message"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	CreatedAt     time.Time  `json:"created_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
	DeliveredAt   *time.Time `json:"delivered_at"`
	LastError     string     `json:"last_error"`
	RetryOf       *int64     `json:"retry_of"`
}
type NotificationDeliveryPage struct {
	Deliveries []NotificationDelivery `json:"deliveries"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}
type notificationDeliveryCursor struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Channel string `json:"channel"`
}

var ErrNotificationCursor = errors.New("invalid notification delivery cursor")

func ListNotificationDeliveries(ctx context.Context, db *DB, userID uuid.UUID, status string, channelID *uuid.UUID, cursor string, limit int) (*NotificationDeliveryPage, error) {
	switch status {
	case "", "pending", "sending", "retry", "succeeded", "failed", "canceled":
	default:
		return nil, ErrNotificationCursor
	}
	if limit < 1 || limit > 200 {
		limit = 50
	}
	args := []any{userID, limit + 1}
	filter := ""
	if status != "" {
		args = append(args, status)
		filter += " AND status=$" + strconv.Itoa(len(args))
	}
	channel := ""
	if channelID != nil {
		channel = channelID.String()
		args = append(args, *channelID)
		filter += " AND channel_id=$" + strconv.Itoa(len(args))
	}
	if cursor != "" {
		if len(cursor) > 512 {
			return nil, ErrNotificationCursor
		}
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, ErrNotificationCursor
		}
		var parsed notificationDeliveryCursor
		if json.Unmarshal(raw, &parsed) != nil || parsed.ID <= 0 || parsed.Status != status || parsed.Channel != channel {
			return nil, ErrNotificationCursor
		}
		args = append(args, parsed.ID)
		filter += " AND id<$" + strconv.Itoa(len(args))
	}
	rows, err := db.Raw.QueryContext(ctx, `SELECT id,channel_id,channel_name,channel_type,event_id,rule_id,server_id,transition,
        COALESCE(payload->>'rule',''),COALESCE(payload->>'message',''),status,attempts,
        created_at,last_attempt_at,CASE WHEN status IN ('pending','retry','sending') THEN next_attempt_at END,delivered_at,last_error,retry_of
        FROM notification_deliveries WHERE user_id=$1`+filter+` ORDER BY id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &NotificationDeliveryPage{Deliveries: []NotificationDelivery{}}
	for rows.Next() {
		var d NotificationDelivery
		if err := rows.Scan(&d.ID, &d.ChannelID, &d.ChannelName, &d.ChannelType, &d.EventID, &d.RuleID, &d.ServerID, &d.Transition, &d.Subject, &d.Message, &d.Status, &d.Attempts,
			&d.CreatedAt, &d.LastAttemptAt, &d.NextAttemptAt, &d.DeliveredAt, &d.LastError, &d.RetryOf); err != nil {
			return nil, err
		}
		page.Deliveries = append(page.Deliveries, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Deliveries) > limit {
		page.Deliveries = page.Deliveries[:limit]
		raw, _ := json.Marshal(notificationDeliveryCursor{page.Deliveries[limit-1].ID, status, channel})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}

// enqueueChannelNotifications shares the alert event transaction. Reading the
// rule associations in that transaction avoids stale engine config snapshots.
func enqueueChannelNotifications(ctx context.Context, tx *sql.Tx, eventID int64, userID, ruleID, serverID uuid.UUID, n *AlertNotification) (bool, error) {
	var associated bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM alert_rule_channels WHERE rule_id=$1)`, ruleID).Scan(&associated); err != nil {
		return false, err
	}
	if !associated {
		return false, nil
	}
	// Coordinate with disable/delete before inserting any pending rows. A
	// concurrent channel edit then either precedes this read or cancels the
	// inserted rows after this transaction releases its channel locks.
	locked, err := tx.QueryContext(ctx, `SELECT c.id FROM notification_channels c JOIN alert_rule_channels rc ON rc.channel_id=c.id
	 WHERE rc.rule_id=$1 AND c.user_id=$2 AND c.enabled ORDER BY c.id FOR SHARE OF c`, ruleID, userID)
	if err != nil {
		return false, err
	}
	for locked.Next() {
		var id uuid.UUID
		if err = locked.Scan(&id); err != nil {
			locked.Close()
			return false, err
		}
	}
	err = locked.Err()
	locked.Close()
	if err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO notification_deliveries(user_id,channel_id,channel_name,channel_type,event_id,rule_id,server_id,transition,payload)
        SELECT $1,c.id,c.name,c.type,$2,$3,$4,$5,$6 FROM notification_channels c
        JOIN alert_rule_channels rc ON rc.channel_id=c.id AND rc.rule_id=$3 WHERE c.user_id=$1 AND c.enabled
        ON CONFLICT(event_id,channel_id,transition) WHERE retry_of IS NULL AND event_id IS NOT NULL DO NOTHING`,
		userID, eventID, ruleID, serverID, n.Transition, string(n.Payload))
	return true, err
}

// EnqueueUserNotification is shared by non-alert producers, including network
// checks. A nil channel list selects all enabled channels owned by the user.
func EnqueueUserNotification(ctx context.Context, db *DB, userID uuid.UUID, channelIDs []uuid.UUID, transition string, payload json.RawMessage) ([]int64, error) {
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids, err := EnqueueUserNotificationTx(ctx, tx, userID, channelIDs, transition, payload)
	if err != nil {
		return nil, err
	}
	return ids, tx.Commit()
}

// EnqueueUserNotificationTx lets producers commit their state change and its
// notifications together. A failed queue insert must not consume a transition.
func EnqueueUserNotificationTx(ctx context.Context, tx *sql.Tx, userID uuid.UUID, channelIDs []uuid.UUID, transition string, payload json.RawMessage) ([]int64, error) {
	if !json.Valid(payload) || len(payload) > 65536 {
		return nil, errors.New("invalid notification payload")
	}
	if transition != "test" && transition != "firing" && transition != "resolved" {
		return nil, errors.New("invalid notification transition")
	}
	var source struct {
		Key string `json:"source_key"`
	}
	if err := json.Unmarshal(payload, &source); err != nil || len(source.Key) > 128 {
		return nil, errors.New("invalid notification source")
	}
	filter := ""
	args := []any{userID}
	if channelIDs != nil {
		if len(channelIDs) > 32 {
			return nil, ErrNotificationChannels
		}
		args = append(args, pq.Array(channelIDs))
		filter = " AND c.id=ANY($2::uuid[])"
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM notification_channels c WHERE user_id=$1 AND enabled`+filter+` FOR SHARE`, args...)
	if err != nil {
		return nil, err
	}
	var selected []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		selected = append(selected, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if channelIDs != nil {
		owned := map[uuid.UUID]bool{}
		for _, id := range selected {
			owned[id] = true
		}
		for _, id := range channelIDs {
			if !owned[id] {
				return nil, ErrNotificationDisabled
			}
		}
	}
	ids := []int64{}
	for _, id := range selected {
		var deliveryID int64
		if err = tx.QueryRowContext(ctx, `INSERT INTO notification_deliveries(user_id,channel_id,channel_name,channel_type,transition,payload,source_key)
            SELECT user_id,id,name,type,$3,$4,$5 FROM notification_channels WHERE id=$1 AND user_id=$2
            RETURNING id`, id, userID, transition, string(payload), source.Key).Scan(&deliveryID); err != nil {
			return nil, err
		}
		ids = append(ids, deliveryID)
	}
	return ids, nil
}

// A manual retry is a new, auditable send, never an overwrite of history. If
// the original was still queued it is canceled atomically to prevent two jobs.
func RetryNotificationDelivery(ctx context.Context, db *DB, id int64, userID uuid.UUID) (int64, error) {
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var channelID *uuid.UUID
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT channel_id,status FROM notification_deliveries WHERE id=$1 AND user_id=$2`, id, userID).Scan(&channelID, &state); err != nil {
		return 0, err
	}
	if channelID == nil {
		return 0, ErrNotificationDisabled
	}
	if state == "sending" {
		return 0, ErrNotificationConflict
	}
	var enabled bool
	if err = tx.QueryRowContext(ctx, `SELECT enabled FROM notification_channels WHERE id=$1 AND user_id=$2 FOR SHARE`, channelID, userID).Scan(&enabled); err != nil {
		return 0, err
	}
	if !enabled {
		return 0, ErrNotificationDisabled
	}
	// Same lock order as channel updates: channel first, delivery second.
	if err = tx.QueryRowContext(ctx, `SELECT status FROM notification_deliveries WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID).Scan(&state); err != nil {
		return 0, err
	}
	if state == "sending" {
		return 0, ErrNotificationConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,last_error='Replaced by manual retry'
        WHERE id=$1 AND status IN ('pending','retry')`, id); err != nil {
		return 0, err
	}
	var next int64
	err = tx.QueryRowContext(ctx, `INSERT INTO notification_deliveries(user_id,channel_id,channel_name,channel_type,event_id,rule_id,server_id,transition,payload,retry_of,source_key)
        SELECT d.user_id,c.id,c.name,c.type,d.event_id,d.rule_id,d.server_id,d.transition,d.payload,COALESCE(d.retry_of,d.id),d.source_key
        FROM notification_deliveries d JOIN notification_channels c ON c.id=d.channel_id
        WHERE d.id=$1 RETURNING id`, id).Scan(&next)
	if err != nil {
		return 0, err
	}
	return next, tx.Commit()
}

type PendingNotification struct {
	ID           int64
	UserID       uuid.UUID
	ChannelID    uuid.UUID
	Payload      json.RawMessage
	Attempts     int
	LeaseVersion int64
}

func ClaimNotifications(ctx context.Context, db *DB, limit int) ([]PendingNotification, error) {
	rows, err := db.Raw.QueryContext(ctx, `UPDATE notification_deliveries SET status='sending',attempts=attempts+1,
        lease_version=lease_version+1,last_attempt_at=NOW(),next_attempt_at=NOW()+INTERVAL '1 minute'
        WHERE id IN (SELECT d.id FROM notification_deliveries d JOIN notification_channels c ON c.id=d.channel_id AND c.user_id=d.user_id
        WHERE c.enabled AND d.status IN ('pending','retry','sending') AND d.next_attempt_at<=NOW()
        AND NOT EXISTS(SELECT 1 FROM notification_deliveries earlier WHERE earlier.channel_id=d.channel_id
            AND earlier.source_key=d.source_key
            AND earlier.rule_id IS NOT DISTINCT FROM d.rule_id AND earlier.server_id IS NOT DISTINCT FROM d.server_id
            AND earlier.status IN ('pending','retry','sending')
            AND (COALESCE(earlier.retry_of,earlier.id),earlier.id)<(COALESCE(d.retry_of,d.id),d.id))
        ORDER BY d.id LIMIT $1 FOR UPDATE OF d SKIP LOCKED)
        RETURNING id,user_id,channel_id,payload,attempts,lease_version`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []PendingNotification{}
	for rows.Next() {
		var item PendingNotification
		if err := rows.Scan(&item.ID, &item.UserID, &item.ChannelID, &item.Payload, &item.Attempts, &item.LeaseVersion); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func FinishNotification(ctx context.Context, db *DB, item PendingNotification, succeeded bool, safeError string, retryAfter time.Duration) error {
	state := "retry"
	if succeeded {
		state = "succeeded"
		safeError = ""
	}
	// Callers supply only fixed error categories/codes, never a remote body,
	// raw URL, credentials, or an arbitrary network exception string.
	if len(safeError) > 256 {
		safeError = "Notification delivery failed"
	}
	_, err := db.Raw.ExecContext(ctx, `UPDATE notification_deliveries SET status=$1::varchar,last_error=$2,
        delivered_at=CASE WHEN $1::varchar='succeeded' THEN NOW() ELSE NULL END,next_attempt_at=NOW()+($3*INTERVAL '1 second')
        WHERE id=$4 AND lease_version=$5 AND status='sending'`, state, safeError, int(retryAfter.Seconds()), item.ID, item.LeaseVersion)
	return err
}
