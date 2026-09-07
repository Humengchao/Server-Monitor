package models

import "database/sql"

type DB struct {
	Raw           *sql.DB
	EncryptionKey string
}
