package database

import (
	"database/sql"
	_ "embed"
	"fmt"

	_ "github.com/lib/pq"
)

//go:embed migrations.sql
var migrationsSQL string

//go:embed access.sql
var accessSQL string

//go:embed notifications.sql
var notificationsSQL string

//go:embed network.sql
var networkSQL string

//go:embed ai_providers.sql
var aiProvidersSQL string

//go:embed ai_tasks.sql
var aiTasksSQL string

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
	_, err := db.Exec(migrationsSQL + "\n" + accessSQL + "\n" + notificationsSQL + "\n" + networkSQL + "\n" + aiProvidersSQL + "\n" + aiTasksSQL)
	if err != nil {
		return fmt.Errorf("exec migrations: %w", err)
	}
	return nil
}
