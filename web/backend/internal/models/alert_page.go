package models

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

var ErrAlertCursor = errors.New("invalid alert cursor")

type AlertEventPage struct {
	Events     []AlertEvent `json:"events"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type alertEventCursor struct {
	At    time.Time `json:"at"`
	ID    int64     `json:"id"`
	State string    `json:"state"`
}

func parseAlertCursor(encoded, state string) (*alertEventCursor, error) {
	if encoded == "" {
		return nil, nil
	}
	if len(encoded) > 512 {
		return nil, ErrAlertCursor
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrAlertCursor
	}
	var cursor alertEventCursor
	if json.Unmarshal(raw, &cursor) != nil || cursor.ID <= 0 || cursor.At.IsZero() || cursor.State != state {
		return nil, ErrAlertCursor
	}
	return &cursor, nil
}

// Pages use a stable timestamp/id order. Active and resolved histories have
// independent filters, so a large number of firing events cannot hide history.
func GetAlertEventPage(ctx context.Context, db *sql.DB, userID uuid.UUID, state string, limit int, encoded string) (*AlertEventPage, error) {
	filter := ""
	switch state {
	case "all":
	case "active":
		filter = " AND e.resolved_at IS NULL"
	case "resolved":
		filter = " AND e.resolved_at IS NOT NULL"
	default:
		return nil, fmt.Errorf("invalid alert state")
	}
	if limit < 1 || limit > 200 {
		limit = 100
	}
	cursor, err := parseAlertCursor(encoded, state)
	if err != nil {
		return nil, err
	}
	args := []any{userID, limit + 1}
	if cursor != nil {
		filter += " AND (e.started_at, e.id) < ($3, $4)"
		args = append(args, cursor.At, cursor.ID)
	}
	rows, err := db.QueryContext(ctx, `SELECT e.id, e.rule_id, COALESCE(r.name, ''), COALESCE(r.metric, ''),
		e.server_id, COALESCE(s.name, ''), e.value, e.message, COALESCE(r.comparator, '>'),
		COALESCE(r.threshold, 0), COALESCE(r.duration_seconds, 0), e.started_at, e.resolved_at
		FROM alert_events e
		LEFT JOIN alert_rules r ON r.id=e.rule_id AND r.user_id=e.user_id
		LEFT JOIN servers s ON s.id=e.server_id AND s.user_id=e.user_id
		WHERE e.user_id=$1`+filter+`
		ORDER BY e.started_at DESC, e.id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &AlertEventPage{Events: []AlertEvent{}}
	for rows.Next() {
		var event AlertEvent
		var resolved sql.NullTime
		if err := rows.Scan(&event.ID, &event.RuleID, &event.RuleName, &event.Metric, &event.ServerID, &event.ServerName,
			&event.Value, &event.Message, &event.Comparator, &event.Threshold, &event.Duration, &event.StartedAt, &resolved); err != nil {
			return nil, err
		}
		if resolved.Valid {
			event.ResolvedAt = &resolved.Time
		}
		page.Events = append(page.Events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Events) > limit {
		page.Events = page.Events[:limit]
		last := page.Events[limit-1]
		raw, _ := json.Marshal(alertEventCursor{At: last.StartedAt, ID: last.ID, State: state})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
