package models

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func notificationTestChannel(t *testing.T, db *DB, user uuid.UUID, name string) NotificationChannel {
	t.Helper()
	c := NotificationChannel{UserID: user, Name: name, Type: "webhook", Enabled: true, Config: NotificationConfig{WebhookURL: "https://example.invalid?secret=token", BearerToken: "private-bearer"}}
	if err := CreateNotificationChannel(context.Background(), db, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestNotificationFanoutIsAtomicOrderedAndTenantIsolated(t *testing.T) {
	raw := testdb.Open(t)
	db := &DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	ctx := context.Background()
	user, server, rule := testdb.Seed(t, raw)
	other, _, _ := testdb.Seed(t, raw)
	a := notificationTestChannel(t, db, user, "A")
	b := notificationTestChannel(t, db, user, "B")
	foreign := notificationTestChannel(t, db, other, "foreign")
	var stored string
	if err := raw.QueryRow(`SELECT config_encrypted FROM notification_channels WHERE id=$1`, a.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "private-bearer") || strings.Contains(stored, "example.invalid") {
		t.Fatal("plaintext configuration stored")
	}
	ruleData := AlertRule{ID: rule, UserID: user, Name: "CPU", ServerID: &server, Metric: "cpu", Comparator: ">", Threshold: 90, Duration: 30, Enabled: true, ChannelIDs: []uuid.UUID{a.ID, foreign.ID}}
	if err := UpdateAlertRule(raw, &ruleData); !errors.Is(err, ErrNotificationChannels) {
		t.Fatalf("foreign channel: %v", err)
	}
	ruleData.ChannelIDs = []uuid.UUID{a.ID, b.ID}
	if err := UpdateAlertRule(raw, &ruleData); err != nil {
		t.Fatal(err)
	}
	event := &AlertEvent{UserID: user, RuleID: rule, ServerID: server, StartedAt: time.Now()}
	if _, err := OpenAlertEventWithNotification(ctx, raw, event, &AlertNotification{Transition: "firing", Payload: json.RawMessage(`bad`)}); err == nil {
		t.Fatal("invalid fanout payload committed")
	}
	var count int
	_ = raw.QueryRow(`SELECT COUNT(*) FROM alert_events`).Scan(&count)
	if count != 0 {
		t.Fatal("event escaped failed outbox transaction")
	}
	n := &AlertNotification{Transition: "firing", Payload: json.RawMessage(`{"status":"firing"}`)}
	if _, err := OpenAlertEventWithNotification(ctx, raw, event, n); err != nil {
		t.Fatal(err)
	}
	n.Transition = "resolved"
	n.Payload = json.RawMessage(`{"status":"resolved"}`)
	if _, err := ResolveAlertEventWithNotification(ctx, raw, rule, server, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	claimed := make(chan []PendingNotification, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			items, err := ClaimNotifications(ctx, db, 4)
			if err != nil {
				t.Error(err)
			}
			claimed <- items
		}()
	}
	group.Wait()
	close(claimed)
	first := []PendingNotification{}
	for items := range claimed {
		first = append(first, items...)
	}
	if len(first) != 2 {
		t.Fatalf("expected one firing per channel: %+v", first)
	}
	for _, item := range first {
		if string(item.Payload) != `{"status": "firing"}` && string(item.Payload) != `{"status":"firing"}` {
			t.Fatal("recovery passed firing")
		}
		if err := FinishNotification(ctx, db, item, true, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	recovered, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(recovered) != 2 {
		t.Fatalf("recovery %v %v", recovered, err)
	}
	page, err := ListNotificationDeliveries(ctx, db, other, "", nil, "", 1)
	if err != nil || len(page.Deliveries) != 0 {
		t.Fatal("cross-user deliveries leaked")
	}
	page, err = ListNotificationDeliveries(ctx, db, user, "", nil, "", 2)
	if err != nil || len(page.Deliveries) != 2 || page.NextCursor == "" {
		t.Fatal("missing first page")
	}
	second, err := ListNotificationDeliveries(ctx, db, user, "", nil, page.NextCursor, 2)
	if err != nil || len(second.Deliveries) != 2 || second.Deliveries[0].ID >= page.Deliveries[1].ID {
		t.Fatal("unstable delivery paging")
	}
	if _, err := ListNotificationDeliveries(ctx, db, user, "retry", nil, page.NextCursor, 2); !errors.Is(err, ErrNotificationCursor) {
		t.Fatal("cursor reused with different filter")
	}
}

func TestNotificationEditsInvalidateLeasesAndRetriesPreserveHistory(t *testing.T) {
	raw := testdb.Open(t)
	db := &DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	ctx := context.Background()
	user, _, _ := testdb.Seed(t, raw)
	c := notificationTestChannel(t, db, user, "channel")
	ids, err := EnqueueUserNotification(ctx, db, user, []uuid.UUID{c.ID}, "test", json.RawMessage(`{"status":"test"}`))
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	first, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	c.Config.BearerToken = "replacement"
	if err := UpdateNotificationChannel(ctx, db, &c); err != nil {
		t.Fatal(err)
	}
	if err := FinishNotification(ctx, db, first[0], true, "", 0); err != nil {
		t.Fatal(err)
	}
	second, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(second) != 1 || second[0].LeaseVersion == first[0].LeaseVersion {
		t.Fatal("stale worker completed retargeted job")
	}
	if err := FinishNotification(ctx, db, second[0], true, "", 0); err != nil {
		t.Fatal(err)
	}
	retry, err := RetryNotificationDelivery(ctx, db, ids[0], user)
	if err != nil || retry == ids[0] {
		t.Fatal("manual retry did not create new delivery", err)
	}
	c.Enabled = false
	if err := UpdateNotificationChannel(ctx, db, &c); err != nil {
		t.Fatal(err)
	}
	items, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(items) != 0 {
		t.Fatal("disabled channel delivered", err)
	}
	if _, err := EnqueueUserNotification(ctx, db, user, []uuid.UUID{c.ID}, "test", json.RawMessage(`{}`)); !errors.Is(err, ErrNotificationDisabled) {
		t.Fatal("disabled test queued", err)
	}
	if err := DeleteNotificationChannel(ctx, db, c.ID, user); err != nil {
		t.Fatal(err)
	}
	page, err := ListNotificationDeliveries(ctx, db, user, "", nil, "", 50)
	if err != nil || len(page.Deliveries) != 2 {
		t.Fatal("history lost after channel deletion")
	}
	if page.Deliveries[0].Status != "canceled" || page.Deliveries[1].Status != "succeeded" || page.Deliveries[0].ChannelID != nil {
		t.Fatalf("unexpected history %+v", page.Deliveries)
	}
}

func TestNotificationManualRetryKeepsTransitionOrderAndExpiredLeaseRecovers(t *testing.T) {
	raw := testdb.Open(t)
	db := &DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	ctx := context.Background()
	user, server, rule := testdb.Seed(t, raw)
	c := notificationTestChannel(t, db, user, "channel")
	r := AlertRule{ID: rule, UserID: user, Name: "CPU", ServerID: &server, Metric: "cpu", Comparator: ">", Threshold: 90, Duration: 30, Enabled: true, ChannelIDs: []uuid.UUID{c.ID}}
	if err := UpdateAlertRule(raw, &r); err != nil {
		t.Fatal(err)
	}
	n := &AlertNotification{Transition: "firing", Payload: json.RawMessage(`{"status":"firing"}`)}
	if _, err := OpenAlertEventWithNotification(ctx, raw, &AlertEvent{UserID: user, RuleID: rule, ServerID: server, StartedAt: time.Now()}, n); err != nil {
		t.Fatal(err)
	}
	n.Transition = "resolved"
	n.Payload = json.RawMessage(`{"status":"resolved"}`)
	if _, err := ResolveAlertEventWithNotification(ctx, raw, rule, server, time.Now(), n); err != nil {
		t.Fatal(err)
	}
	first, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(first) != 1 {
		t.Fatal(first, err)
	}
	if err := FinishNotification(ctx, db, first[0], false, "failure", time.Minute); err != nil {
		t.Fatal(err)
	}
	next, err := RetryNotificationDelivery(ctx, db, first[0].ID, user)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(retry) != 1 || retry[0].ID != next {
		t.Fatal("recovery overtook manual firing retry", retry, err)
	}
	if _, err := raw.Exec(`UPDATE notification_deliveries SET next_attempt_at=NOW()-INTERVAL '1 second' WHERE id=$1`, next); err != nil {
		t.Fatal(err)
	}
	recovered, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(recovered) != 1 || recovered[0].ID != next || recovered[0].LeaseVersion == retry[0].LeaseVersion {
		t.Fatal("expired lease was not recovered", recovered, err)
	}
	if err := FinishNotification(ctx, db, retry[0], true, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := FinishNotification(ctx, db, recovered[0], true, "", 0); err != nil {
		t.Fatal(err)
	}
	resolved, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(resolved) != 1 || !strings.Contains(string(resolved[0].Payload), "resolved") {
		t.Fatal("recovery missing after firing delivered", resolved, err)
	}
}
