package models

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func TestDeleteServerReturnsNotFoundForUnownedOrMissingRows(t *testing.T) {
	db := testdb.Open(t)
	user, own, _ := testdb.Seed(t, db)
	other, foreign, _ := testdb.Seed(t, db)
	for _, id := range []uuid.UUID{foreign, uuid.New()} {
		if err := DeleteServer(db, id, user); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("unowned deletion returned %v", err)
		}
	}
	if err := DeleteServer(db, own, other); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign owner deletion returned %v", err)
	}
	if err := DeleteServer(db, own, user); err != nil {
		t.Fatal(err)
	}
	if err := DeleteServer(db, own, user); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("repeated deletion succeeded", err)
	}
	var remains bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM servers WHERE id=$1)`, foreign).Scan(&remains); err != nil || !remains {
		t.Fatal("foreign server changed", err)
	}
}

func TestBulkDeleteReturnsOnlyCommittedOwnedIDs(t *testing.T) {
	db := testdb.Open(t)
	user, own, _ := testdb.Seed(t, db)
	_, foreign, _ := testdb.Seed(t, db)
	deleted, err := DeleteServersReturningIDs(context.Background(), db, []uuid.UUID{foreign, own, uuid.New(), own}, user)
	if err != nil || len(deleted) != 1 || deleted[0] != own {
		t.Fatalf("wrong affected IDs: %v %v", deleted, err)
	}
	deleted, err = DeleteServersReturningIDs(context.Background(), db, []uuid.UUID{foreign, own}, user)
	if err != nil || len(deleted) != 0 {
		t.Fatal("unowned/repeated IDs reported as deleted", deleted, err)
	}
	var remains bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM servers WHERE id=$1)`, foreign).Scan(&remains); err != nil || !remains {
		t.Fatal("foreign server changed", err)
	}
}
