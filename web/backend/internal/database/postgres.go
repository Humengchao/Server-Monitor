package database

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "github.com/lib/pq"
)

//go:embed migrations.sql
var migrationsSQL string

func Connect(databaseURL string) (*sql.DB, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return db, nil
}

func RunMigrations(db *sql.DB) error {
	_, err := db.Exec(migrationsSQL)
	if err != nil {
		return fmt.Errorf("exec migrations: %w", err)
	}
	return nil
}
