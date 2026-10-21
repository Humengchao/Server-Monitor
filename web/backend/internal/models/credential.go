package models

import (
	"context"
	"database/sql"
	"fmt"

	"server-monitor/internal/crypto"

	"github.com/google/uuid"
)

type Credential struct {
	ID            uuid.UUID `json:"id"`
	UserID        uuid.UUID `json:"user_id"`
	Name          string    `json:"name"`
	SSHUsername   string    `json:"ssh_username"`
	SSHPassword   string    `json:"-"`
	SSHKey        string    `json:"-"`
	HasPassword   bool      `json:"has_password"`
	HasKey        bool      `json:"has_key"`
	SSHAuthMethod string    `json:"ssh_auth_method"`
	CredType      string    `json:"credential_type"`
	CreatedAt     string    `json:"created_at"`
}

func CreateCredential(db *DB, c *Credential, options ...SSHAuthUpdate) error {
	c.ID = uuid.New()
	var update SSHAuthUpdate
	if len(options) > 0 {
		update = options[0]
	}
	var err error
	c.SSHPassword, c.SSHKey, err = mergeSSHAuthentication("", "", c.SSHPassword, c.SSHKey, c.CredType, update)
	if err != nil {
		return err
	}
	encPassword, err := crypto.Encrypt(c.SSHPassword, db.EncryptionKey)
	if err != nil {
		return err
	}
	encKey, err := crypto.Encrypt(c.SSHKey, db.EncryptionKey)
	if err != nil {
		return err
	}
	c.HasPassword = encPassword != ""
	c.HasKey = encKey != ""
	c.SSHAuthMethod = EffectiveSSHAuthMethod(encPassword, encKey)
	return db.Raw.QueryRow(
		`INSERT INTO credentials (id, user_id, name, ssh_username, ssh_password, ssh_key, credential_type)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`,
		c.ID, c.UserID, c.Name, c.SSHUsername, encPassword, encKey, c.CredType,
	).Scan(&c.CreatedAt)
}

func GetCredentialsByUserID(db *sql.DB, userID uuid.UUID) ([]Credential, error) {
	rows, err := db.Query(
		`SELECT id, user_id, name, ssh_username,
		        COALESCE(ssh_password, '') <> '', COALESCE(ssh_key, '') <> '',
		        COALESCE(credential_type, 'linux'), created_at
		 FROM credentials WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.SSHUsername, &c.HasPassword, &c.HasKey, &c.CredType, &c.CreatedAt); err != nil {
			return nil, err
		}
		if c.HasPassword {
			c.SSHAuthMethod = "password"
		} else if c.HasKey {
			c.SSHAuthMethod = "key"
		}
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

func GetCredentialByID(db *DB, id, userID uuid.UUID) (*Credential, error) {
	return GetCredentialByIDContext(context.Background(), db, id, userID)
}

func GetCredentialByIDContext(ctx context.Context, db *DB, id, userID uuid.UUID) (*Credential, error) {
	c := &Credential{}
	var encPassword, encKey string
	err := db.Raw.QueryRowContext(ctx,
		`SELECT id, user_id, name, ssh_username, ssh_password, ssh_key, COALESCE(credential_type, 'linux'), created_at
		 FROM credentials WHERE id=$1 AND user_id=$2`, id, userID,
	).Scan(&c.ID, &c.UserID, &c.Name, &c.SSHUsername, &encPassword, &encKey, &c.CredType, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.HasPassword = encPassword != ""
	c.HasKey = encKey != ""
	c.SSHAuthMethod = EffectiveSSHAuthMethod(encPassword, encKey)
	c.SSHPassword, err = crypto.Decrypt(encPassword, db.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential password: %w", err)
	}
	c.SSHKey, err = crypto.Decrypt(encKey, db.EncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt credential key: %w", err)
	}
	return c, nil
}

func UpdateCredential(db *DB, c *Credential, options ...SSHAuthUpdate) error {
	tx, err := db.Raw.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldPassword, oldKey string
	if err := tx.QueryRow(`SELECT COALESCE(ssh_password,''),COALESCE(ssh_key,'') FROM credentials WHERE id=$1 AND user_id=$2 FOR UPDATE`, c.ID, c.UserID).Scan(&oldPassword, &oldKey); err != nil {
		return err
	}
	password, err := crypto.Encrypt(c.SSHPassword, db.EncryptionKey)
	if err != nil {
		return err
	}
	key, err := crypto.Encrypt(c.SSHKey, db.EncryptionKey)
	if err != nil {
		return err
	}
	var update SSHAuthUpdate
	if len(options) > 0 {
		update = options[0]
	}
	password, key, err = mergeSSHAuthentication(oldPassword, oldKey, password, key, c.CredType, update)
	if err != nil {
		return err
	}

	result, err := tx.Exec(
		`UPDATE credentials SET name=$1, ssh_username=$2, credential_type=$3, ssh_password=$6, ssh_key=$7 WHERE id=$4 AND user_id=$5`,
		c.Name, c.SSHUsername, c.CredType, c.ID, c.UserID, password, key)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	c.HasPassword, c.HasKey = password != "", key != ""
	c.SSHAuthMethod = EffectiveSSHAuthMethod(password, key)
	return tx.Commit()
}

func DeleteCredential(db *sql.DB, id, userID uuid.UUID) error {
	_, err := db.Exec("DELETE FROM credentials WHERE id=$1 AND user_id=$2", id, userID)
	return err
}
