package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func TestNotificationDispatcherRetriesAfterRestartWithStableID(t *testing.T) {
	raw := testdb.Open(t)
	db := &models.DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	ctx := context.Background()
	user, _, _ := testdb.Seed(t, raw)
	var calls atomic.Int32
	identities := make(chan string, 2)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identities <- r.Header.Get("X-Server-Monitor-Delivery")
		if calls.Add(1) == 1 {
			w.WriteHeader(500)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer endpoint.Close()
	channel := models.NotificationChannel{UserID: user, Name: "local", Type: "webhook", Enabled: true, Config: models.NotificationConfig{WebhookURL: endpoint.URL}}
	if err := models.CreateNotificationChannel(ctx, db, &channel); err != nil {
		t.Fatal(err)
	}
	ids, err := models.EnqueueUserNotification(ctx, db, user, []uuid.UUID{channel.ID}, "test", json.RawMessage(`{"status":"test"}`))
	if err != nil {
		t.Fatal(err)
	}
	first := &NotificationDispatcher{db: db, sender: NewNotificationSender(true)}
	first.deliver(ctx)
	var status string
	var attempts int
	if err := raw.QueryRow(`SELECT status,attempts FROM notification_deliveries WHERE id=$1`, ids[0]).Scan(&status, &attempts); err != nil || status != "retry" || attempts != 1 {
		t.Fatal(status, attempts, err)
	}
	if _, err := raw.Exec(`UPDATE notification_deliveries SET next_attempt_at=NOW()`); err != nil {
		t.Fatal(err)
	}
	restarted := &NotificationDispatcher{db: db, sender: NewNotificationSender(true)}
	restarted.deliver(ctx)
	if err := raw.QueryRow(`SELECT status,attempts FROM notification_deliveries WHERE id=$1`, ids[0]).Scan(&status, &attempts); err != nil || status != "succeeded" || attempts != 2 {
		t.Fatal(status, attempts, err)
	}
	a, b := <-identities, <-identities
	if a == "" || a != b {
		t.Fatal("retry delivery identity changed")
	}
}
