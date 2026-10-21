package models

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func TestPublicAvailabilityCensusClampsCreationAndRechecksVisibility(t *testing.T) {
	db := testdb.Open(t)
	user, public, _ := testdb.Seed(t, db)
	private := uuid.New()
	end := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	created := end.Add(-115 * time.Minute)
	if _, err := db.Exec(`UPDATE servers SET created_at=$1 WHERE id=$2`, created, public); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO servers(id,user_id,name,host,is_public,created_at) VALUES($1,$2,'private','private.invalid',FALSE,$3)`, private, user, created); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{public, private} {
		for _, at := range []time.Time{end.Add(-2 * time.Hour), end.Add(-105 * time.Minute), end.Add(-15 * time.Minute), end} {
			if _, err := db.Exec(`INSERT INTO server_metrics_15m(server_id,recorded_at,sample_count) VALUES($1,$2,1)`, id, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	counts, err := GetPublicAvailabilityContext(ctx, db, []uuid.UUID{public, private}, end.Add(-30*24*time.Hour), end)
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[public].Observed != 2 || counts[public].Expected != 7 {
		t.Fatalf("census=%+v", counts)
	}
	if _, ok := counts[private]; ok {
		t.Fatal("private server entered public census")
	}
}
