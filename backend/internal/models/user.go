package models

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID           uuid.UUID `json:"id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

func CreateUser(db *sql.DB, username, passwordHash string) (*User, error) {
	u := &User{ID: uuid.New(), Username: username, PasswordHash: passwordHash}
	err := db.QueryRow(
		"INSERT INTO users (id, username, password_hash) VALUES ($1, $2, $3) RETURNING created_at",
		u.ID, username, passwordHash,
	).Scan(&u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func GetUserByUsername(db *sql.DB, username string) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		"SELECT id, username, password_hash, created_at FROM users WHERE username=$1",
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func GetUserByID(db *sql.DB, id uuid.UUID) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		"SELECT id, username, password_hash, created_at FROM users WHERE id=$1",
		id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}
