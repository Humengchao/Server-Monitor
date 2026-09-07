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

func GetLoginHistory(db *sql.DB, userID uuid.UUID, limit, offset int) ([]LoginHistory, error) {
	rows, err := db.Query(
		`SELECT id, user_id, ip, user_agent, success, logged_at
		 FROM login_history
		 WHERE user_id=$1
		 ORDER BY logged_at DESC
		 LIMIT $2 OFFSET $3`,
		userID, limit, offset)
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

func CountLoginHistory(db *sql.DB, userID uuid.UUID) (int, error) {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM login_history WHERE user_id=$1", userID).Scan(&count)
	return count, err
}
