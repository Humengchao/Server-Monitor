package models

import (
	"database/sql"
	"fmt"

	"server-monitor/internal/crypto"

	"github.com/google/uuid"
)

type Credential struct {
	ID          uuid.UUID `json:"id"`
	UserID      uuid.UUID `json:"user_id"`
	Name        string    `json:"name"`
	SSHUsername string    `json:"ssh_username"`
	SSHPassword string    `json:"-"`
	SSHKey      string    `json:"-"`
	CredType    string    `json:"credential_type"`
	CreatedAt   string    `json:"created_at"`
}

func CreateCredential(db *DB, c *Credential) error {
	c.ID = uuid.New()
	encPassword, err := crypto.Encrypt(c.SSHPassword, db.EncryptionKey)
	if err != nil {
		return err
	}
	encKey, err := crypto.Encrypt(c.SSHKey, db.EncryptionKey)
	if err != nil {
		return err
	}
	return db.Raw.QueryRow(
		`INSERT INTO credentials (id, user_id, name, ssh_username, ssh_password, ssh_key, credential_type)
		 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`,
		c.ID, c.UserID, c.Name, c.SSHUsername, encPassword, encKey, c.CredType,
	).Scan(&c.CreatedAt)
}

func GetCredentialsByUserID(db *sql.DB, userID uuid.UUID) ([]Credential, error) {
	rows, err := db.Query(
		`SELECT id, user_id, name, ssh_username, COALESCE(credential_type, 'linux'), created_at
		 FROM credentials WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.SSHUsername, &c.CredType, &c.CreatedAt); err != nil {
			return nil, err
		}
		creds = append(creds, c)
	}
	return creds, nil
}

func GetCredentialByID(db *DB, id, userID uuid.UUID) (*Credential, error) {
	c := &Credential{}
	var encPassword, encKey string
	err := db.Raw.QueryRow(
		`SELECT id, user_id, name, ssh_username, ssh_password, ssh_key, COALESCE(credential_type, 'linux'), created_at
		 FROM credentials WHERE id=$1 AND user_id=$2`, id, userID,
	).Scan(&c.ID, &c.UserID, &c.Name, &c.SSHUsername, &encPassword, &encKey, &c.CredType, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
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

func UpdateCredential(db *DB, c *Credential) error {
	tx, err := db.Raw.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`UPDATE credentials SET name=$1, ssh_username=$2, credential_type=$3 WHERE id=$4 AND user_id=$5`,
		c.Name, c.SSHUsername, c.CredType, c.ID, c.UserID)
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
	if c.SSHPassword != "" {
		enc, err := crypto.Encrypt(c.SSHPassword, db.EncryptionKey)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE credentials SET ssh_password=$1 WHERE id=$2 AND user_id=$3`, enc, c.ID, c.UserID)
		if err != nil {
			return err
		}
	}
	if c.SSHKey != "" {
		enc, err := crypto.Encrypt(c.SSHKey, db.EncryptionKey)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE credentials SET ssh_key=$1 WHERE id=$2 AND user_id=$3`, enc, c.ID, c.UserID)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func DeleteCredential(db *sql.DB, id, userID uuid.UUID) error {
	_, err := db.Exec("DELETE FROM credentials WHERE id=$1 AND user_id=$2", id, userID)
	return err
}
