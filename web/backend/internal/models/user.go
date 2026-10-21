package models

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID               uuid.UUID `json:"id"`
	Username         string    `json:"username"`
	PasswordHash     string    `json:"-"`
	CreatedAt        time.Time `json:"created_at"`
	TokensValidAfter time.Time `json:"-"`
}

// UpdateUserPassword stores a new hash and moves the token cutoff to now,
// which invalidates every JWT issued before this moment — that is what makes a
// password change sign other devices out.
//
// Tokens carry this exact microsecond version separately from the standard
// second-resolution iat, so even two password changes in one second revoke
// the preceding session. A stale password verification cannot overwrite a
// concurrent password change.
func UpdateUserPassword(db *sql.DB, id uuid.UUID, passwordHash string, previousHash ...string) (time.Time, error) {
	var validAfter time.Time
	var expected any
	if len(previousHash) > 0 {
		expected = previousHash[0]
	}
	err := db.QueryRow(
		`UPDATE users SET password_hash=$1, tokens_valid_after=GREATEST(clock_timestamp(),tokens_valid_after+INTERVAL '1 microsecond')
		 WHERE id=$2 AND ($3::text IS NULL OR password_hash=$3) RETURNING tokens_valid_after`,
		passwordHash, id, expected,
	).Scan(&validAfter)
	return validAfter, err
}

// GetTokensValidAfter returns the revocation cutoff for a user. It is read on
// every authenticated request, so it is a single primary-key lookup.
func GetTokensValidAfter(db *sql.DB, id uuid.UUID) (time.Time, error) {
	return GetTokensValidAfterContext(context.Background(), db, id)
}

func GetTokensValidAfterContext(ctx context.Context, db *sql.DB, id uuid.UUID) (time.Time, error) {
	var validAfter time.Time
	err := db.QueryRowContext(ctx, `SELECT tokens_valid_after FROM users WHERE id=$1`, id).Scan(&validAfter)
	return validAfter, err
}

func CreateUser(db *sql.DB, username, passwordHash string) (*User, error) {
	u := &User{ID: uuid.New(), Username: username, PasswordHash: passwordHash}
	err := db.QueryRow(
		"INSERT INTO users (id, username, password_hash) VALUES ($1, $2, $3) RETURNING created_at,tokens_valid_after",
		u.ID, username, passwordHash,
	).Scan(&u.CreatedAt, &u.TokensValidAfter)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func GetUserByUsername(db *sql.DB, username string) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		"SELECT id, username, password_hash, created_at,tokens_valid_after FROM users WHERE username=$1",
		username,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.TokensValidAfter)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func GetUserByID(db *sql.DB, id uuid.UUID) (*User, error) {
	u := &User{}
	err := db.QueryRow(
		"SELECT id, username, password_hash, created_at,tokens_valid_after FROM users WHERE id=$1",
		id,
	).Scan(&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.TokensValidAfter)
	if err != nil {
		return nil, err
	}
	return u, nil
}
