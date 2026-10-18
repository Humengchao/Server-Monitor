package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func TestNotesPatchPreservesConcurrentConnectionEdit(t *testing.T) {
	db := testdb.Open(t)
	user, id, _ := testdb.Seed(t, db)
	if _, err := db.Exec(`UPDATE servers SET name='edited',host='192.0.2.2',port=2222 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err := UpdateServerNotes(context.Background(), db, id, user, "new notes"); err != nil {
		t.Fatal(err)
	}
	var name, host, notes string
	var port int
	if err := db.QueryRow(`SELECT name,host,port,notes FROM servers WHERE id=$1`, id).Scan(&name, &host, &port, &notes); err != nil {
		t.Fatal(err)
	}
	if name != "edited" || host != "192.0.2.2" || port != 2222 || notes != "new notes" {
		t.Fatalf("other fields changed: %s %s %d %s", name, host, port, notes)
	}
	if err := UpdateServerNotes(context.Background(), db, id, uuid.New(), "foreign"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("foreign update: %v", err)
	}
	if err := UpdateServerNotes(context.Background(), db, id, user, ""); err != nil {
		t.Fatal(err)
	}
}

func TestAlertCursorPagesAllEventsAndSeparateHistory(t *testing.T) {
	db := testdb.Open(t)
	user, server, rule := testdb.Seed(t, db)
	// Each active event needs a different rule/server pair. Identical timestamps
	// exercise the ID tie breaker instead of relying on clock spacing.
	if _, err := db.Exec(`INSERT INTO alert_rules(id,user_id,name,metric)
		SELECT md5('rule-'||n)::uuid,$1,'rule','cpu' FROM generate_series(1,205)n`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO alert_events(rule_id,server_id,user_id,started_at)
		SELECT md5('rule-'||n)::uuid,$2,$1,'2026-01-01'::timestamptz FROM generate_series(1,205)n`, user, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO alert_events(rule_id,server_id,user_id,started_at,resolved_at) VALUES($1,$2,$3,NOW(),NOW())`, rule, server, user); err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	cursor := ""
	for {
		page, err := GetAlertEventPage(context.Background(), db, user, "active", 100, cursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Events {
			if seen[event.ID] {
				t.Fatalf("duplicate %d", event.ID)
			}
			seen[event.ID] = true
			if event.ResolvedAt != nil {
				t.Fatal("resolved in active page")
			}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(seen) != 205 {
		t.Fatalf("got %d active events", len(seen))
	}
	history, err := GetAlertEventPage(context.Background(), db, user, "resolved", 100, "")
	if err != nil || len(history.Events) != 1 {
		t.Fatalf("history: %v %v", history, err)
	}
	foreign, err := GetAlertEventPage(context.Background(), db, uuid.New(), "all", 100, "")
	if err != nil || len(foreign.Events) != 0 {
		t.Fatalf("foreign: %v %v", foreign, err)
	}
}

func TestAlertOutboxCommitsAtomicallyAndClaimsInOrder(t *testing.T) {
	db := testdb.Open(t)
	user, server, rule := testdb.Seed(t, db)
	ctx := context.Background()
	event := &AlertEvent{UserID: user, ServerID: server, RuleID: rule, StartedAt: time.Now(), Value: 99}
	invalid := &AlertNotification{URL: "https://example.invalid", Transition: "firing", Payload: json.RawMessage(`invalid JSON`)}
	if _, err := OpenAlertEventWithNotification(ctx, db, event, invalid); err == nil {
		t.Fatal("invalid notification must roll back event")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM alert_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("event escaped transaction: %d %v", count, err)
	}
	notification := &AlertNotification{URL: "https://example.invalid", Transition: "firing", Payload: json.RawMessage(`{"status":"firing"}`)}
	id, err := OpenAlertEventWithNotification(ctx, db, event, notification)
	if err != nil || id == 0 {
		t.Fatalf("open %d %v", id, err)
	}
	duplicate, err := OpenAlertEventWithNotification(ctx, db, event, notification)
	if err != nil || duplicate != 0 {
		t.Fatalf("duplicate %d %v", duplicate, err)
	}
	notification.Transition = "resolved"
	notification.Payload = json.RawMessage(`{"status":"resolved"}`)
	if ok, err := ResolveAlertEventWithNotification(ctx, db, rule, server, time.Now(), notification); err != nil || !ok {
		t.Fatalf("resolve: %v %v", ok, err)
	}
	var wg sync.WaitGroup
	claims := make(chan []PendingWebhook, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := ClaimAlertWebhooks(ctx, db, 4)
			if err != nil {
				t.Error(err)
			}
			claims <- items
		}()
	}
	wg.Wait()
	close(claims)
	var first []PendingWebhook
	for items := range claims {
		first = append(first, items...)
	}
	if len(first) != 1 {
		t.Fatalf("only firing can be claimed once: %v", first)
	}
	if err := FinishAlertWebhook(ctx, db, first[0], false, 0); err != nil {
		t.Fatal(err)
	}
	retry, err := ClaimAlertWebhooks(ctx, db, 4)
	if err != nil || len(retry) != 1 || retry[0].ID != first[0].ID || retry[0].Attempts != 2 {
		t.Fatalf("retry %v %v", retry, err)
	}
	if err := FinishAlertWebhook(ctx, db, retry[0], true, 0); err != nil {
		t.Fatal(err)
	}
	recovery, err := ClaimAlertWebhooks(ctx, db, 4)
	if err != nil || len(recovery) != 1 || recovery[0].ID == first[0].ID {
		t.Fatalf("recovery %v %v", recovery, err)
	}
	if _, err := db.Exec(`UPDATE alert_webhooks SET next_attempt_at=NOW()-INTERVAL '1 second' WHERE id=$1`, recovery[0].ID); err != nil {
		t.Fatal(err)
	}
	afterRestart, err := ClaimAlertWebhooks(ctx, db, 4)
	if err != nil || len(afterRestart) != 1 || afterRestart[0].Attempts != 2 {
		t.Fatalf("expired lease not recovered %v %v", afterRestart, err)
	}
	updated := &AlertRule{ID: rule, UserID: user, Name: "CPU", ServerID: &server, Metric: "cpu", Comparator: ">", Enabled: true, WebhookURL: "https://new.example.invalid"}
	if err := UpdateAlertRule(db, updated); err != nil {
		t.Fatal(err)
	}
	if err := FinishAlertWebhook(ctx, db, afterRestart[0], true, 0); err != nil {
		t.Fatal(err)
	}
	retargeted, err := ClaimAlertWebhooks(ctx, db, 4)
	if err != nil || len(retargeted) != 1 || retargeted[0].Target != updated.WebhookURL || retargeted[0].Attempts != 1 {
		t.Fatalf("new URL remained blocked: %v %v", retargeted, err)
	}
	updated.Enabled = false
	if err := UpdateAlertRule(db, updated); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM alert_webhooks WHERE delivered_at IS NULL`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled rule still queued: %d %v", count, err)
	}
}
