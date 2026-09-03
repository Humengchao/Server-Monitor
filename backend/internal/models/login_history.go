package models

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type LoginHistory struct {
	ID        int64     `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	IP        string    `json:"ip"`
	UserAgent string    `json:"user_agent"`
	Success   bool      `json:"success"`
	LoggedAt  time.Time `json:"logged_at"`
}

func InsertLoginRecord(db *sql.DB, userID uuid.UUID, ip, userAgent string, success bool) error {
	_, err := db.Exec(
		"INSERT INTO login_history (user_id, ip, user_agent, success) VALUES ($1, $2, $3, $4)",
		userID, ip, userAgent, success)
	return err
}

func GetLastLogin(db *sql.DB, userID uuid.UUID) (*LoginHistory, error) {
	h := &LoginHistory{}
	err := db.QueryRow(
		`SELECT id, user_id, ip, user_agent, success, logged_at
		 FROM login_history
		 WHERE user_id=$1 AND success=TRUE
		 ORDER BY logged_at DESC LIMIT 1`,
		userID,
	).Scan(&h.ID, &h.UserID, &h.IP, &h.UserAgent, &h.Success, &h.LoggedAt)
	if err != nil {
		return nil, err
	}
	return h, nil
}

// GetLoginHistory lists a user's sign-in attempts, newest first. failedOnly
// narrows to rejected attempts — the reason this log exists is to spot someone
// guessing at your password, and a burst of failures is otherwise buried under
// pages of routine successes.
func GetLoginHistory(db *sql.DB, userID uuid.UUID, limit, offset int, failedOnly bool) ([]LoginHistory, error) {
	rows, err := db.Query(
		`SELECT id, user_id, ip, user_agent, success, logged_at
		 FROM login_history
		 WHERE user_id=$1 AND ($4 = FALSE OR success = FALSE)
		 ORDER BY logged_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset, failedOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []LoginHistory
	for rows.Next() {
		var h LoginHistory
		if err := rows.Scan(&h.ID, &h.UserID, &h.IP, &h.UserAgent, &h.Success, &h.LoggedAt); err != nil {
			return nil, err
		}
		records = append(records, h)
	}
	return records, nil
}

// CountLoginHistory returns the total attempts and how many of them failed.
// The failure count is reported alongside every page so the UI can show it
// without walking the whole table.
func CountLoginHistory(db *sql.DB, userID uuid.UUID) (total, failed int, err error) {
	err = db.QueryRow(
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE success = FALSE)
		 FROM login_history WHERE user_id=$1`, userID).Scan(&total, &failed)
	return total, failed, err
}
