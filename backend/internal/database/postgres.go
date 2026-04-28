package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	_ "github.com/lib/pq"
)

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
	_, filename, _, _ := runtime.Caller(0)
	migrationFile := filepath.Join(filepath.Dir(filename), "migrations.sql")
	sqlBytes, err := os.ReadFile(migrationFile)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	_, err = db.Exec(string(sqlBytes))
	if err != nil {
		return fmt.Errorf("exec migrations: %w", err)
	}
	return nil
}
