package models

import (
	"database/sql"

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
		`INSERT INTO credentials (id, user_id, name, ssh_username, ssh_password, ssh_key)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING created_at`,
		c.ID, c.UserID, c.Name, c.SSHUsername, encPassword, encKey,
	).Scan(&c.CreatedAt)
}

func GetCredentialsByUserID(db *sql.DB, userID uuid.UUID) ([]Credential, error) {
	rows, err := db.Query(
		`SELECT id, user_id, name, ssh_username, created_at
		 FROM credentials WHERE user_id=$1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var creds []Credential
	for rows.Next() {
		var c Credential
		if err := rows.Scan(&c.ID, &c.UserID, &c.Name, &c.SSHUsername, &c.CreatedAt); err != nil {
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
		`SELECT id, user_id, name, ssh_username, ssh_password, ssh_key, created_at
		 FROM credentials WHERE id=$1 AND user_id=$2`, id, userID,
	).Scan(&c.ID, &c.UserID, &c.Name, &c.SSHUsername, &encPassword, &encKey, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.SSHPassword, _ = crypto.Decrypt(encPassword, db.EncryptionKey)
	c.SSHKey, _ = crypto.Decrypt(encKey, db.EncryptionKey)
	return c, nil
}

// GetCredentialByIDInternal is used internally (e.g., collector) without user check.
func GetCredentialByIDInternal(db *DB, id uuid.UUID) (*Credential, error) {
	c := &Credential{}
	var encPassword, encKey string
	err := db.Raw.QueryRow(
		`SELECT id, user_id, name, ssh_username, ssh_password, ssh_key, created_at
		 FROM credentials WHERE id=$1`, id,
	).Scan(&c.ID, &c.UserID, &c.Name, &c.SSHUsername, &encPassword, &encKey, &c.CreatedAt)
	if err != nil {
		return nil, err
	}
	c.SSHPassword, _ = crypto.Decrypt(encPassword, db.EncryptionKey)
	c.SSHKey, _ = crypto.Decrypt(encKey, db.EncryptionKey)
	return c, nil
}

func UpdateCredential(db *DB, c *Credential) error {
	_, err := db.Raw.Exec(
		`UPDATE credentials SET name=$1, ssh_username=$2 WHERE id=$3 AND user_id=$4`,
		c.Name, c.SSHUsername, c.ID, c.UserID)
	if err != nil {
		return err
	}
	if c.SSHPassword != "" {
		enc, err := crypto.Encrypt(c.SSHPassword, db.EncryptionKey)
		if err != nil {
			return err
		}
		_, err = db.Raw.Exec(`UPDATE credentials SET ssh_password=$1 WHERE id=$2`, enc, c.ID)
		if err != nil {
			return err
		}
	}
	if c.SSHKey != "" {
		enc, err := crypto.Encrypt(c.SSHKey, db.EncryptionKey)
		if err != nil {
			return err
		}
		_, err = db.Raw.Exec(`UPDATE credentials SET ssh_key=$1 WHERE id=$2`, enc, c.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func DeleteCredential(db *sql.DB, id, userID uuid.UUID) error {
	_, err := db.Exec("DELETE FROM credentials WHERE id=$1 AND user_id=$2", id, userID)
	return err
}
