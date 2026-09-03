package models

import (
	"database/sql"
	"errors"
	"time"

	"github.com/google/uuid"
)

// UserSettings holds one user's preferences. A user with no row has never
// opened the settings page; the zero value is the correct answer for them, so
// nothing here is nullable.
type UserSettings struct {
	DefaultWebhookURL string    `json:"default_webhook_url"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// GetUserSettings returns a user's preferences, or the zero value when they
// have never saved any. Absent settings are not an error.
func GetUserSettings(db *sql.DB, userID uuid.UUID) (UserSettings, error) {
	var s UserSettings
	err := db.QueryRow(
		"SELECT default_webhook_url, updated_at FROM user_settings WHERE user_id=$1", userID,
	).Scan(&s.DefaultWebhookURL, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return UserSettings{}, nil
	}
	if err != nil {
		return UserSettings{}, err
	}
	return s, nil
}

func SaveUserSettings(db *sql.DB, userID uuid.UUID, s UserSettings) error {
	_, err := db.Exec(
		`INSERT INTO user_settings (user_id, default_webhook_url, updated_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (user_id) DO UPDATE SET
		   default_webhook_url = EXCLUDED.default_webhook_url,
		   updated_at = EXCLUDED.updated_at`,
		userID, s.DefaultWebhookURL)
	return err
}

// GetDefaultWebhooks returns every non-empty default webhook keyed by user.
// The alert engine reads this once per evaluation cycle rather than joining it
// onto the rule query, so a user with no default costs nothing.
func GetDefaultWebhooks(db *sql.DB) (map[uuid.UUID]string, error) {
	rows, err := db.Query(
		"SELECT user_id, default_webhook_url FROM user_settings WHERE default_webhook_url <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	defaults := make(map[uuid.UUID]string)
	for rows.Next() {
		var id uuid.UUID
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			return nil, err
		}
		defaults[id] = url
	}
	return defaults, rows.Err()
}
