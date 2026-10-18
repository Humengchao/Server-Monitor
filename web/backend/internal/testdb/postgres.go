// Package testdb creates isolated schemas only in an explicitly configured
// test database. It never reads the application's DATABASE_URL.
package testdb

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/database"
)

func Open(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SERVER_MONITOR_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SERVER_MONITOR_TEST_DATABASE_URL not set; PostgreSQL integration runs in CI")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "sm_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parameters := parsed.Query()
	parameters.Set("search_path", name)
	parsed.RawQuery = parameters.Encode()
	db, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+name+" CASCADE"); err != nil {
			t.Errorf("clean test schema: %v", err)
		}
		admin.Close()
	})
	if err = database.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func Seed(t *testing.T, db *sql.DB) (userID, serverID, ruleID uuid.UUID) {
	t.Helper()
	userID, serverID, ruleID = uuid.New(), uuid.New(), uuid.New()
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO users(id,username,password_hash) VALUES($1,$2,'test')`, []any{userID, userID.String()}},
		{`INSERT INTO servers(id,user_id,name,host,notes) VALUES($1,$2,'test','192.0.2.1','original')`, []any{serverID, userID}},
		{`INSERT INTO alert_rules(id,user_id,name,server_id,metric,webhook_url) VALUES($1,$2,'CPU',$3,'cpu','https://example.invalid')`, []any{ruleID, userID, serverID}},
	} {
		if _, err := db.Exec(query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	return
}
