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

// UpdateUserPassword stores a new hash and moves the token cutoff to now,
// which invalidates every JWT issued before this moment — that is what makes a
// password change sign other devices out.
//
// The cutoff is truncated to whole seconds because JWT "iat" has one-second
// resolution: a sub-second cutoff would reject the very token issued next.
// The returned time is the exact "iat" to mint the caller's replacement token
// with, so their current session survives.
func UpdateUserPassword(db *sql.DB, id uuid.UUID, passwordHash string) (time.Time, error) {
	var validAfter time.Time
	err := db.QueryRow(
		`UPDATE users SET password_hash=$1, tokens_valid_after=date_trunc('second', NOW())
		 WHERE id=$2 RETURNING tokens_valid_after`,
		passwordHash, id,
	).Scan(&validAfter)
	return validAfter, err
}

// GetTokensValidAfter returns the revocation cutoff for a user. It is read on
// every authenticated request, so it is a single primary-key lookup.
func GetTokensValidAfter(db *sql.DB, id uuid.UUID) (time.Time, error) {
	var validAfter time.Time
	err := db.QueryRow(`SELECT tokens_valid_after FROM users WHERE id=$1`, id).Scan(&validAfter)
	return validAfter, err
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
