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

func TestNetworkNotificationFailureRollsBackSampleAndCanRetry(t *testing.T) {
	raw := testdb.Open(t)
	db := &DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	ctx := context.Background()
	user, server, _ := testdb.Seed(t, raw)
	_ = notificationTestChannel(t, db, user, "channel")
	check := NetworkCheck{ID: uuid.New(), UserID: user, Name: "network", TargetServerID: server, Packets: 3, Enabled: true, LossThreshold: 10}
	if err := SaveNetworkCheck(ctx, raw, &check, true); err != nil {
		t.Fatal(err)
	}
	sent, received, loss := 3, 0, 100.0
	sample := NetworkSample{RecordedAt: time.Now().UTC(), Status: "down", Sent: &sent, Received: &received, LossPercent: &loss}
	// Force the actual outbox INSERT to fail after the sample INSERT. The
	// producer must not consume the transition by committing just its sample.
	if _, err := raw.Exec(`ALTER TABLE notification_deliveries ADD CONSTRAINT reject_fixture_delivery CHECK (source_key='never')`); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertNetworkSampleWithNotification(ctx, db, check, sample, json.RawMessage(`{}`)); err == nil {
		t.Fatal("queue failure was swallowed")
	}
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM network_samples`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("sample committed without delivery: %d %v", count, err)
	}
	if _, err := raw.Exec(`ALTER TABLE notification_deliveries DROP CONSTRAINT reject_fixture_delivery`); err != nil {
		t.Fatal(err)
	}
	saved, err := InsertNetworkSampleWithNotification(ctx, db, check, sample, json.RawMessage(`{}`))
	if err != nil || !saved.ThresholdExceeded {
		t.Fatal("retry did not commit firing state", err)
	}
	first, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(first) != 1 {
		t.Fatal("transition lost after queue recovery", first, err)
	}
	if err := FinishNotification(ctx, db, first[0], false, "fixture failure", time.Minute); err != nil {
		t.Fatal(err)
	}
	received = 3
	loss = 0
	sample.RecordedAt = sample.RecordedAt.Add(time.Minute)
	sample.Status = "ok"
	if _, err := InsertNetworkSampleWithNotification(ctx, db, check, sample, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	ready, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(ready) != 0 {
		t.Fatal("recovery overtook failed firing", ready, err)
	}
	if _, err := raw.Exec(`UPDATE notification_deliveries SET next_attempt_at=NOW() WHERE id=$1`, first[0].ID); err != nil {
		t.Fatal(err)
	}
	retry, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(retry) != 1 || retry[0].ID != first[0].ID {
		t.Fatal(retry, err)
	}
	if err := FinishNotification(ctx, db, retry[0], true, "", 0); err != nil {
		t.Fatal(err)
	}
	resolved, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(resolved) != 1 {
		t.Fatal(resolved, err)
	}
	var payload map[string]any
	_ = json.Unmarshal(resolved[0].Payload, &payload)
	if payload["status"] != "resolved" || payload["source_key"] != "network:"+check.ID.String() {
		t.Fatal("incorrect recovery payload", payload)
	}
}

func TestNetworkNotificationsSerializeConcurrentTransitionsAndSeparateSources(t *testing.T) {
	raw := testdb.Open(t)
	db := &DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	ctx := context.Background()
	user, server, _ := testdb.Seed(t, raw)
	_ = notificationTestChannel(t, db, user, "channel")
	check := NetworkCheck{ID: uuid.New(), UserID: user, Name: "network", TargetServerID: server, Packets: 3, Enabled: true, LossThreshold: 10}
	if err := SaveNetworkCheck(ctx, raw, &check, true); err != nil {
		t.Fatal(err)
	}
	sent, received, loss := 3, 0, 100.0
	at := time.Now().UTC()
	var group sync.WaitGroup
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			_, _ = InsertNetworkSampleWithNotification(ctx, db, check, NetworkSample{RecordedAt: at.Add(time.Duration(i) * time.Millisecond), Status: "down", Sent: &sent, Received: &received, LossPercent: &loss}, json.RawMessage(`{}`))
		}(i)
	}
	group.Wait()
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate concurrent transition", count, err)
	}
	second := check
	second.ID = uuid.New()
	if err := SaveNetworkCheck(ctx, raw, &second, true); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertNetworkSampleWithNotification(ctx, db, second, NetworkSample{RecordedAt: at, Status: "down", Sent: &sent, Received: &received, LossPercent: &loss}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	items, err := ClaimNotifications(ctx, db, 4)
	if err != nil || len(items) != 2 {
		t.Fatal("independent checks block one another", items, err)
	}
	check.Enabled = false
	if err := SaveNetworkCheck(ctx, raw, &check, false); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE source_key=$1 AND status='canceled'`, "network:"+check.ID.String()).Scan(&count); err != nil || count != 1 {
		t.Fatal("disabled check has live queue", count, err)
	}
}

func TestNetworkMissingRTTCannotResolveALatencyAlert(t *testing.T) {
	threshold := 100.0
	zero := 0.0
	sent := 3
	check := NetworkCheck{LossThreshold: 10, LatencyThresholdMS: &threshold, Latest: &NetworkSample{ThresholdExceeded: true}}
	partial := NetworkSample{Sent: &sent, LossPercent: &zero}
	if transition := NetworkSampleTransition(check, &partial); transition != "" || !partial.ThresholdExceeded {
		t.Fatal("missing RTT falsely recovered latency alert")
	}
	latency := 20.0
	partial.AvgMS = &latency
	if transition := NetworkSampleTransition(check, &partial); transition != "resolved" || partial.ThresholdExceeded {
		t.Fatal("known healthy RTT did not recover")
	}
}

func TestNetworkProbeCannotPublishAfterTargetMonitoringIsPaused(t *testing.T) {
	raw := testdb.Open(t)
	db := &DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	ctx := context.Background()
	user, server, _ := testdb.Seed(t, raw)
	_ = notificationTestChannel(t, db, user, "channel")
	check := NetworkCheck{ID: uuid.New(), UserID: user, Name: "network", TargetServerID: server, Packets: 3, Enabled: true, LossThreshold: 10}
	if err := SaveNetworkCheck(ctx, raw, &check, true); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE servers SET monitoring_enabled=FALSE WHERE id=$1`, server); err != nil {
		t.Fatal(err)
	}
	sent, received, loss := 3, 0, 100.0
	_, err := InsertNetworkSampleWithNotification(ctx, db, check, NetworkSample{RecordedAt: time.Now(), Status: "down", Sent: &sent, Received: &received, LossPercent: &loss}, json.RawMessage(`{}`))
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("paused target published in-flight probe", err)
	}
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM notification_deliveries`).Scan(&count); err != nil || count != 0 {
		t.Fatal("paused target queued an alert", count, err)
	}
}
