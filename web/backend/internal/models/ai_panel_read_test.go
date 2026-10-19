package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"server-monitor/internal/testdb"
)

func aiPanelDecode(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func aiPanelExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func TestAIPanelFleetSummaryCoversWholeAccountAndOmitsSecrets(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	_, foreign, _ := testdb.Seed(t, db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	aiPanelExec(t, db, `UPDATE servers SET name='A-first',ssh_username='SECRET-user',ssh_password='SECRET-password',ssh_key='SECRET-key',ssh_host_key='SECRET-hostkey',last_error='SECRET-diagnostic',disk_total_bytes=1000 WHERE id=$1`, server)
	if err := SaveMetric(db, server, &MetricPoint{CPUPercent: 10, MemoryUsed: 200, MemoryTotal: 1000, DiskUsed: 300, LatencyMS: 15, RecordedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		aiPanelExec(t, db, `INSERT INTO servers(id,user_id,name,host) VALUES($1,$2,$3,'SECRET-address')`, uuid.New(), user, fmt.Sprintf("N-%02d", i))
	}
	worst := uuid.New()
	aiPanelExec(t, db, `INSERT INTO servers(id,user_id,name,host,disk_total_bytes) VALUES($1,$2,'ZZ-worst','SECRET-address',1000)`, worst, user)
	if err := SaveMetric(db, worst, &MetricPoint{CPUPercent: 99, MemoryUsed: 980, MemoryTotal: 1000, DiskUsed: 990, LatencyMS: 700, RecordedAt: now}); err != nil {
		t.Fatal(err)
	}
	page, err := GetAIPanelFleet(ctx, db, user, now, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 57 || len(page.Items) != 50 || page.NextOffset == nil || *page.NextOffset != 50 {
		t.Fatalf("bad pagination: %+v", page.AIPanelPage)
	}
	summary := aiPanelDecode(t, page.Summary)
	if summary["fresh"] != float64(2) || summary["unknown"] != float64(55) || summary["fresh_cpu_max_percent"] != float64(99) || summary["fresh_high_cpu_90_percent"] != float64(1) {
		t.Fatalf("page-scoped summary: %s", page.Summary)
	}
	second, err := GetAIPanelFleet(ctx, db, user, now, 50, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 7 || second.NextOffset != nil {
		t.Fatal("invalid last page")
	}
	encoded, _ := json.Marshal(page)
	for _, secret := range []string{"SECRET-", foreign.String(), "ssh_", "webhook", "user_id"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("projection leaked %s: %s", secret, encoded)
		}
	}
	// The global high-water marks remain visible even when the affected host
	// is after the first page and lower-priority unknown hosts fill top_issues.
	if aiPanelDecode(t, second.Summary)["fresh_disk_max_percent"] != float64(99) {
		t.Fatal("lost account-wide disk peak")
	}
	if len(page.TopResources) != 4 {
		t.Fatal("missing independent resource leaders", page.TopResources)
	}
	for _, raw := range page.TopResources {
		if aiPanelDecode(t, raw)["id"] != worst.String() {
			t.Fatal("resource leader hidden beyond page", string(raw))
		}
	}
	if _, err := GetAIPanelServer(ctx, db, user, foreign, now); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign server exposed", err)
	}
}

func TestAIPanelFreshnessAndUnknownCapacity(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, mode string
		enabled    bool
		age        time.Duration
		sample     bool
		want       string
	}{
		{"fresh", "ssh", true, 119 * time.Second, true, "fresh"},
		{"boundary", "ssh", true, 120 * time.Second, true, "offline"},
		{"future", "ssh", true, -31 * time.Second, true, "unknown"},
		{"allowed-skew", "ssh", true, -30 * time.Second, true, "fresh"},
		{"paused", "ssh", false, time.Hour, true, "paused"},
		{"agent-pending", "agent", true, 0, false, "pending"},
		{"no-data", "ssh", true, 0, false, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			aiPanelExec(t, db, `UPDATE servers SET connection_mode=$2,monitoring_enabled=$3 WHERE id=$1`, server, tc.mode, tc.enabled)
			aiPanelExec(t, db, `DELETE FROM server_latest_metrics WHERE server_id=$1`, server)
			if tc.sample {
				if err := SaveMetric(db, server, &MetricPoint{RecordedAt: now.Add(-tc.age)}); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := GetAIPanelServer(ctx, db, user, server, now)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := aiPanelDecode(t, raw)["server"].(map[string]any)
			if snapshot["status"] != tc.want {
				t.Fatalf("wanted %s: %s", tc.want, raw)
			}
			if snapshot["memory_percent"] != nil || snapshot["disk_percent"] != nil {
				t.Fatalf("unknown capacity became zero percent: %s", raw)
			}
			if !tc.sample && snapshot["cpu_percent"] != nil {
				t.Fatalf("missing CPU sample became zero: %s", raw)
			}
		})
	}
}

func TestAIPanelHistoryWeightedRollupsBoundedAndScoped(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	_, foreign, _ := testdb.Seed(t, db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Minute)
	// The older tier averages 10 samples at 10% and 30 samples at 90%.
	for _, sample := range []struct {
		value, count int
		at           time.Time
	}{{10, 10, now.Add(-48 * time.Hour)}, {90, 30, now.Add(-48*time.Hour + time.Minute)}} {
		aiPanelExec(t, db, `INSERT INTO server_metrics_1m(server_id,cpu_percent,memory_used,memory_total,disk_used_bytes,latency_ms,sample_count,recorded_at) VALUES($1,$2,0,0,0,0,$3,$4)`, server, sample.value, sample.count, sample.at)
	}
	// A raw row older than the retention seam must not double count its rollup.
	if err := SaveMetric(db, server, &MetricPoint{CPUPercent: 100, RecordedAt: now.Add(-48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	history, err := GetAIPanelHistory(ctx, db, user, server, now, "7d", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Points) != 1 || history.MissingBuckets != 0 || history.ExpectedBuckets != 1 {
		t.Fatalf("bad history %+v", history)
	}
	point := aiPanelDecode(t, history.Points[0])
	if point["samples"] != float64(40) || point["cpu_average_percent"] != float64(70) || point["memory_average_percent"] != nil {
		t.Fatalf("incorrect weighting/unknown denominator: %s", history.Points[0])
	}
	// With no samples in the recent range, all intervals must remain unknown.
	empty, err := GetAIPanelHistory(ctx, db, user, server, now, "1h", 120)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Points) != 0 || empty.MissingBuckets != 120 {
		t.Fatalf("missing intervals filled with zero: %+v", empty)
	}
	if _, err := GetAIPanelHistory(ctx, db, user, foreign, now, "1h", 10); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("foreign history exposed", err)
	}
}

func TestAIPanelAlertsAndChannelsOmitNotificationSecrets(t *testing.T) {
	db := testdb.Open(t)
	user, server, rule := testdb.Seed(t, db)
	other, foreign, otherRule := testdb.Seed(t, db)
	ctx := context.Background()
	now := time.Now().UTC()
	channel := uuid.New()
	foreignChannel := uuid.New()
	for _, c := range []struct {
		id, user uuid.UUID
		name     string
	}{{channel, user, "Telegram"}, {foreignChannel, other, "FOREIGN-channel"}} {
		aiPanelExec(t, db, `INSERT INTO notification_channels(id,user_id,name,type,enabled,config_encrypted) VALUES($1,$2,$3,'telegram',TRUE,'SECRET-notification-config')`, c.id, c.user, c.name)
	}
	aiPanelExec(t, db, `INSERT INTO alert_rule_channels(rule_id,channel_id) VALUES($1,$2)`, rule, channel)
	aiPanelExec(t, db, `UPDATE alert_rules SET webhook_url='https://SECRET-webhook.invalid/token' WHERE id=$1`, rule)
	for _, v := range []struct{ user, server, rule uuid.UUID }{{user, server, rule}, {other, foreign, otherRule}} {
		aiPanelExec(t, db, `INSERT INTO alert_events(rule_id,server_id,user_id,value,message) VALUES($1,$2,$3,95,'SECRET-event-message')`, v.rule, v.server, v.user)
	}
	for _, kind := range []string{"rules", "active"} {
		page, err := GetAIPanelAlerts(ctx, db, user, &server, now, kind, 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 1 || len(page.Items) != 1 {
			t.Fatalf("wrong %s page %+v", kind, page)
		}
		encoded, _ := json.Marshal(page)
		for _, secret := range []string{"SECRET-", foreign.String(), otherRule.String(), foreignChannel.String(), "webhook_url", "config_encrypted"} {
			if strings.Contains(string(encoded), secret) {
				t.Fatalf("%s exposed in %s", secret, encoded)
			}
		}
		if _, err := GetAIPanelAlerts(ctx, db, user, &foreign, now, kind, 0, 20); !errors.Is(err, sql.ErrNoRows) {
			t.Fatal("foreign alert selector accepted", err)
		}
	}
	page, err := GetAIPanelChannels(ctx, db, user, now, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatal("unexpected channels", page)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "SECRET-") || strings.Contains(string(encoded), "FOREIGN-") {
		t.Fatal("channel data leak", string(encoded))
	}
	// A global rule is relevant when the caller asks about one server.
	aiPanelExec(t, db, `UPDATE alert_rules SET server_id=NULL WHERE id=$1`, rule)
	rules, err := GetAIPanelAlerts(ctx, db, user, &server, now, "rules", 0, 20)
	if err != nil || rules.Total != 1 {
		t.Fatal("global rule excluded from server", err, rules)
	}
}
