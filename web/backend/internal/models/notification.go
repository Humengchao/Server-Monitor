package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/crypto"
)

var ErrNotificationConflict = errors.New("notification changed; reload and retry")
var ErrNotificationDisabled = errors.New("notification channel is disabled or deleted")
var ErrNotificationChannels = errors.New("one or more notification channels do not belong to this user")

// NotificationConfig is never serialized as part of a channel API response.
// Entire configs are encrypted, including token-bearing webhook URLs.
type NotificationConfig struct {
	BotToken        string   `json:"bot_token,omitempty"`
	ChatID          string   `json:"chat_id,omitempty"`
	MessageThreadID int64    `json:"message_thread_id,omitempty"`
	WebhookURL      string   `json:"webhook_url,omitempty"`
	Secret          string   `json:"secret,omitempty"`
	BearerToken     string   `json:"bearer_token,omitempty"`
	SMTPHost        string   `json:"smtp_host,omitempty"`
	SMTPPort        int      `json:"smtp_port,omitempty"`
	TLSMode         string   `json:"tls_mode,omitempty"`
	Username        string   `json:"username,omitempty"`
	Password        string   `json:"password,omitempty"`
	From            string   `json:"from,omitempty"`
	To              []string `json:"to,omitempty"`
}

type NotificationChannel struct {
	ID        uuid.UUID          `json:"id"`
	UserID    uuid.UUID          `json:"-"`
	Name      string             `json:"name"`
	Type      string             `json:"type"`
	Enabled   bool               `json:"enabled"`
	Config    NotificationConfig `json:"-"`
	Revision  int64              `json:"-"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

type PublicNotificationChannel struct {
	ID           uuid.UUID          `json:"id"`
	Name         string             `json:"name"`
	Type         string             `json:"type"`
	Enabled      bool               `json:"enabled"`
	Config       NotificationConfig `json:"config"`
	SecretFields []string           `json:"secret_fields"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

func (c NotificationChannel) Public() PublicNotificationChannel {
	config := c.Config
	secrets := []string{}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"bot_token", &config.BotToken}, {"webhook_url", &config.WebhookURL},
		{"secret", &config.Secret}, {"bearer_token", &config.BearerToken}, {"password", &config.Password},
	} {
		if *field.value != "" {
			secrets = append(secrets, field.name)
		}
		*field.value = ""
	}
	return PublicNotificationChannel{ID: c.ID, Name: c.Name, Type: c.Type, Enabled: c.Enabled, Config: config,
		SecretFields: secrets, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// EncryptNotificationConfig and DecryptNotificationConfig are also used by
// password-encrypted backup import/export. Never expose their plaintext through
// ordinary channel or delivery API responses.
func EncryptNotificationConfig(config NotificationConfig, key string) (string, error) {
	raw, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return crypto.Encrypt(string(raw), key)
}
func DecryptNotificationConfig(encoded, key string) (NotificationConfig, error) {
	var config NotificationConfig
	raw, err := crypto.Decrypt(encoded, key)
	if err != nil {
		return config, errors.New("cannot decrypt notification configuration")
	}
	if err = json.Unmarshal([]byte(raw), &config); err != nil {
		return config, errors.New("invalid stored notification configuration")
	}
	return config, nil
}

const notificationChannelSelect = `SELECT id,user_id,name,type,enabled,config_encrypted,revision,created_at,updated_at FROM notification_channels`

type notificationScanner interface{ Scan(...any) error }

func scanNotificationChannel(row notificationScanner, key string) (NotificationChannel, error) {
	var c NotificationChannel
	var encrypted string
	if err := row.Scan(&c.ID, &c.UserID, &c.Name, &c.Type, &c.Enabled, &encrypted, &c.Revision, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return c, err
	}
	var err error
	c.Config, err = DecryptNotificationConfig(encrypted, key)
	return c, err
}
func GetNotificationChannel(ctx context.Context, db *DB, id, userID uuid.UUID) (NotificationChannel, error) {
	return scanNotificationChannel(db.Raw.QueryRowContext(ctx, notificationChannelSelect+` WHERE id=$1 AND user_id=$2`, id, userID), db.EncryptionKey)
}
func ListNotificationChannels(ctx context.Context, db *DB, userID uuid.UUID) ([]NotificationChannel, error) {
	rows, err := db.Raw.QueryContext(ctx, notificationChannelSelect+` WHERE user_id=$1 ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []NotificationChannel{}
	for rows.Next() {
		c, err := scanNotificationChannel(rows, db.EncryptionKey)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
func CreateNotificationChannel(ctx context.Context, db *DB, c *NotificationChannel) error {
	encrypted, err := EncryptNotificationConfig(c.Config, db.EncryptionKey)
	if err != nil {
		return err
	}
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return db.Raw.QueryRowContext(ctx, `INSERT INTO notification_channels(id,user_id,name,type,enabled,config_encrypted)
        VALUES($1,$2,$3,$4,$5,$6) RETURNING revision,created_at,updated_at`, c.ID, c.UserID, c.Name, c.Type, c.Enabled, encrypted).
		Scan(&c.Revision, &c.CreatedAt, &c.UpdatedAt)
}
func UpdateNotificationChannel(ctx context.Context, db *DB, c *NotificationChannel) error {
	encrypted, err := EncryptNotificationConfig(c.Config, db.EncryptionKey)
	if err != nil {
		return err
	}
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `UPDATE notification_channels SET name=$1,enabled=$2,config_encrypted=$3,
        revision=revision+1,updated_at=NOW() WHERE id=$4 AND user_id=$5 AND revision=$6 AND type=$7
        RETURNING revision,created_at,updated_at`, c.Name, c.Enabled, encrypted, c.ID, c.UserID, c.Revision, c.Type).
		Scan(&c.Revision, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return ErrNotificationConflict
	}
	if err != nil {
		return err
	}
	if !c.Enabled {
		_, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,
            last_error='Channel disabled' WHERE channel_id=$1 AND user_id=$2 AND status IN ('pending','retry','sending')`, c.ID, c.UserID)
	} else {
		// A fixed token/URL takes effect on queued attempts. A stale worker's
		// acknowledgement is rejected by its lease version.
		_, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='pending',lease_version=lease_version+1,
            next_attempt_at=NOW(),last_error='' WHERE channel_id=$1 AND user_id=$2 AND status IN ('pending','retry','sending')`, c.ID, c.UserID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func DeleteNotificationChannel(ctx context.Context, db *DB, id, userID uuid.UUID) error {
	tx, err := db.Raw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found uuid.UUID
	if err = tx.QueryRowContext(ctx, `SELECT id FROM notification_channels WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, userID).Scan(&found); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,
        last_error='Channel deleted' WHERE channel_id=$1 AND user_id=$2 AND status IN ('pending','retry','sending')`, id, userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM notification_channels WHERE id=$1 AND user_id=$2`, id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// setAlertRuleChannels is called inside the rule's own transaction, so a
// foreign channel cannot leave a partially updated rule behind.
func setAlertRuleChannels(ctx context.Context, tx *sql.Tx, ruleID, userID uuid.UUID, ids []uuid.UUID) error {
	if ids == nil {
		return nil
	} // older API clients preserve existing associations
	if len(ids) > 32 {
		return fmt.Errorf("at most 32 notification channels per rule")
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var owned uuid.UUID
		if err := tx.QueryRowContext(ctx, `SELECT id FROM notification_channels WHERE id=$1 AND user_id=$2 FOR SHARE`, id, userID).Scan(&owned); err != nil {
			if err == sql.ErrNoRows {
				return ErrNotificationChannels
			}
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM alert_rule_channels WHERE rule_id=$1`, ruleID); err != nil {
		return err
	}
	for id := range seen {
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_rule_channels(rule_id,channel_id) VALUES($1,$2)`, ruleID, id); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE notification_deliveries SET status='canceled',lease_version=lease_version+1,last_error='Channel removed from rule'
        WHERE rule_id=$1 AND user_id=$2 AND status IN ('pending','retry','sending')
        AND NOT EXISTS(SELECT 1 FROM alert_rule_channels rc WHERE rc.rule_id=$1 AND rc.channel_id=notification_deliveries.channel_id)`, ruleID, userID)
	return err
}
