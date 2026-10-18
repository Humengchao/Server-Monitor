package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookDeliveryRetriesWithStableIdentityAfterRestart(t *testing.T) {
	db := testdb.Open(t)
	user, server, rule := testdb.Seed(t, db)
	ids := make(chan string, 2)
	var attempts atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["status"] != "firing" {
			t.Errorf("wire payload: %v %v", body, err)
		}
		ids <- r.Header.Get("X-Server-Monitor-Delivery")
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer endpoint.Close()
	if _, err := db.Exec(`UPDATE alert_rules SET webhook_url=$1 WHERE id=$2`, endpoint.URL, rule); err != nil {
		t.Fatal(err)
	}
	notification := &models.AlertNotification{URL: endpoint.URL, Transition: "firing", Payload: json.RawMessage(`{"status":"firing"}`)}
	_, err := models.OpenAlertEventWithNotification(context.Background(), db, &models.AlertEvent{UserID: user, ServerID: server, RuleID: rule, StartedAt: time.Now()}, notification)
	if err != nil {
		t.Fatal(err)
	}
	first := &WebhookDispatcher{db: db, notifier: NewWebhookNotifier(true)}
	first.deliver(context.Background())
	var delivered bool
	var attempt int
	if err := db.QueryRow(`SELECT delivered_at IS NOT NULL,attempts FROM alert_webhooks`).Scan(&delivered, &attempt); err != nil || delivered || attempt != 1 {
		t.Fatalf("failure lost: %v %d %v", delivered, attempt, err)
	}
	if _, err := db.Exec(`UPDATE alert_webhooks SET next_attempt_at=NOW()`); err != nil {
		t.Fatal(err)
	}
	// A new dispatcher has no in-memory knowledge of the first attempt.
	restarted := &WebhookDispatcher{db: db, notifier: NewWebhookNotifier(true)}
	restarted.deliver(context.Background())
	if err := db.QueryRow(`SELECT delivered_at IS NOT NULL,attempts FROM alert_webhooks`).Scan(&delivered, &attempt); err != nil || !delivered || attempt != 2 {
		t.Fatalf("retry failed: %v %d %v", delivered, attempt, err)
	}
	a, b := <-ids, <-ids
	if a == "" || a != b {
		t.Fatalf("delivery identity changed: %q %q", a, b)
	}
}

func TestWebhookRetryDelayIsBounded(t *testing.T) {
	if webhookRetryDelay(1) != 5*time.Second || webhookRetryDelay(2) != 10*time.Second || webhookRetryDelay(100) != 15*time.Minute {
		t.Fatal("unbounded or incorrect backoff")
	}
}
