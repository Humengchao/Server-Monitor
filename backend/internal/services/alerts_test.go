package services

import (
	"net"
	"strings"
	"testing"
	"time"

	"server-monitor/internal/models"

	"github.com/google/uuid"
)

func snapshot(mutate func(*models.AlertSnapshot)) models.AlertSnapshot {
	s := models.AlertSnapshot{
		ServerID:   uuid.New(),
		UserID:     uuid.New(),
		ServerName: "web-01",
		DiskTotal:  100 * 1024 * 1024 * 1024,
		HasMetrics: true,
		CPUPercent: 10,
		MemoryUsed: 2 * 1024 * 1024 * 1024,
		MemTotal:   8 * 1024 * 1024 * 1024,
		DiskUsed:   40 * 1024 * 1024 * 1024,
		Load1:      0.4,
		LatencyMS:  25,
		RecordedAt: time.Now(),
	}
	if mutate != nil {
		mutate(&s)
	}
	return s
}

func rule(metric, comparator string, threshold float64, duration int) models.AlertRule {
	return models.AlertRule{
		ID:         uuid.New(),
		Name:       "test rule",
		Metric:     metric,
		Comparator: comparator,
		Threshold:  threshold,
		Duration:   duration,
		Enabled:    true,
	}
}

func TestEvaluateRuleThresholds(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		rule      models.AlertRule
		snap      models.AlertSnapshot
		wantState AlertState
		wantValue float64
	}{
		{
			name:      "cpu above threshold breaches",
			rule:      rule(models.AlertMetricCPU, ">", 80, 300),
			snap:      snapshot(func(s *models.AlertSnapshot) { s.CPUPercent = 91.5; s.RecordedAt = now }),
			wantState: AlertBreached,
			wantValue: 91.5,
		},
		{
			name:      "cpu below threshold is clear",
			rule:      rule(models.AlertMetricCPU, ">", 80, 300),
			snap:      snapshot(func(s *models.AlertSnapshot) { s.CPUPercent = 12; s.RecordedAt = now }),
			wantState: AlertClear,
			wantValue: 12,
		},
		{
			name:      "memory percent derived from used over total",
			rule:      rule(models.AlertMetricMemory, ">", 70, 300),
			snap:      snapshot(func(s *models.AlertSnapshot) { s.MemoryUsed = 6 * 1024 * 1024 * 1024; s.RecordedAt = now }),
			wantState: AlertBreached,
			wantValue: 75,
		},
		{
			name:      "disk percent uses the server's total disk size",
			rule:      rule(models.AlertMetricDisk, ">", 85, 300),
			snap:      snapshot(func(s *models.AlertSnapshot) { s.DiskUsed = 90 * 1024 * 1024 * 1024; s.RecordedAt = now }),
			wantState: AlertBreached,
			wantValue: 90,
		},
		{
			name:      "unknown disk size cannot be judged",
			rule:      rule(models.AlertMetricDisk, ">", 85, 300),
			snap:      snapshot(func(s *models.AlertSnapshot) { s.DiskTotal = 0; s.RecordedAt = now }),
			wantState: AlertUnknown,
		},
		{
			name:      "less-than comparator inverts the check",
			rule:      rule(models.AlertMetricLoad1, "<", 1, 300),
			snap:      snapshot(func(s *models.AlertSnapshot) { s.Load1 = 0.2; s.RecordedAt = now }),
			wantState: AlertBreached,
			wantValue: 0.2,
		},
		{
			name:      "missing ping sample is not a zero latency",
			rule:      rule(models.AlertMetricLatency, "<", 5, 300),
			snap:      snapshot(func(s *models.AlertSnapshot) { s.LatencyMS = 0; s.RecordedAt = now }),
			wantState: AlertUnknown,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value, state := EvaluateRule(tc.rule, tc.snap, now)
			if state != tc.wantState {
				t.Fatalf("state = %v, want %v", state, tc.wantState)
			}
			if tc.wantState != AlertUnknown && value != tc.wantValue {
				t.Fatalf("value = %v, want %v", value, tc.wantValue)
			}
		})
	}
}

func TestEvaluateRuleOffline(t *testing.T) {
	now := time.Now()

	// A fresh sample must never trip an offline rule, even a very short one.
	_, state := EvaluateRule(rule(models.AlertMetricOffline, ">", 0, 30),
		snapshot(func(s *models.AlertSnapshot) { s.RecordedAt = now.Add(-10 * time.Second) }), now)
	if state != AlertClear {
		t.Fatalf("fresh sample: state = %v, want AlertClear", state)
	}

	// The floor keeps offline rules from firing before a dashboard card would
	// turn grey, even when the rule asks for less.
	_, state = EvaluateRule(rule(models.AlertMetricOffline, ">", 0, 30),
		snapshot(func(s *models.AlertSnapshot) { s.RecordedAt = now.Add(-90 * time.Second) }), now)
	if state != AlertClear {
		t.Fatalf("below floor: state = %v, want AlertClear", state)
	}

	_, state = EvaluateRule(rule(models.AlertMetricOffline, ">", 0, 30),
		snapshot(func(s *models.AlertSnapshot) { s.RecordedAt = now.Add(-3 * time.Minute) }), now)
	if state != AlertBreached {
		t.Fatalf("past floor: state = %v, want AlertBreached", state)
	}

	// A longer configured window wins over the floor.
	_, state = EvaluateRule(rule(models.AlertMetricOffline, ">", 0, 1800),
		snapshot(func(s *models.AlertSnapshot) { s.RecordedAt = now.Add(-10 * time.Minute) }), now)
	if state != AlertClear {
		t.Fatalf("inside configured window: state = %v, want AlertClear", state)
	}

	// A server that has never reported is offline.
	_, state = EvaluateRule(rule(models.AlertMetricOffline, ">", 0, 300),
		snapshot(func(s *models.AlertSnapshot) { s.HasMetrics = false }), now)
	if state != AlertBreached {
		t.Fatalf("never reported: state = %v, want AlertBreached", state)
	}
}

func TestEvaluateRuleResolvesStaleThresholds(t *testing.T) {
	now := time.Now()
	// A host that stopped reporting while breaching must not keep a CPU alert
	// open forever; past the staleness window the rule reads as clear.
	_, state := EvaluateRule(rule(models.AlertMetricCPU, ">", 50, 300),
		snapshot(func(s *models.AlertSnapshot) { s.CPUPercent = 99; s.RecordedAt = now.Add(-11 * time.Minute) }), now)
	if state != AlertClear {
		t.Fatalf("stale breaching sample: state = %v, want AlertClear", state)
	}

	// Inside the window the last known value still counts.
	_, state = EvaluateRule(rule(models.AlertMetricCPU, ">", 50, 300),
		snapshot(func(s *models.AlertSnapshot) { s.CPUPercent = 99; s.RecordedAt = now.Add(-1 * time.Minute) }), now)
	if state != AlertBreached {
		t.Fatalf("recent breaching sample: state = %v, want AlertBreached", state)
	}
}

func TestFormatAlertMessage(t *testing.T) {
	snap := snapshot(nil)
	msg := FormatAlertMessage(rule(models.AlertMetricCPU, ">", 80, 300), snap, 91.53)
	for _, want := range []string{"web-01", "cpu", "91.5%", "> 80%", "5m"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q missing %q", msg, want)
		}
	}

	offline := FormatAlertMessage(rule(models.AlertMetricOffline, ">", 0, 300), snap, 480)
	if !strings.Contains(offline, "web-01") || !strings.Contains(offline, "8m") {
		t.Fatalf("offline message %q is missing host or duration", offline)
	}
}

func TestFormatRecoveryMessage(t *testing.T) {
	snap := snapshot(nil)
	// The recovery wording must not read as though the threshold is still
	// breached ("cpu is 9% (> 90%)").
	msg := FormatRecoveryMessage(rule(models.AlertMetricCPU, ">", 80, 300), snap, 9)
	if !strings.Contains(msg, "recovered to 9.0%") || !strings.Contains(msg, "threshold > 80%") {
		t.Fatalf("unexpected recovery message %q", msg)
	}

	back := FormatRecoveryMessage(rule(models.AlertMetricOffline, ">", 0, 300), snap, 0)
	if !strings.Contains(back, "web-01") || !strings.Contains(back, "reporting again") {
		t.Fatalf("unexpected offline recovery message %q", back)
	}
}

func TestValidateWebhookURL(t *testing.T) {
	guarded := NewWebhookNotifier(false)
	permissive := NewWebhookNotifier(true)

	if err := guarded.ValidateWebhookURL(""); err != nil {
		t.Fatalf("empty URL should be allowed (webhooks are optional): %v", err)
	}
	if err := guarded.ValidateWebhookURL("ftp://example.com/hook"); err == nil {
		t.Fatal("non-http scheme should be rejected")
	}
	if err := guarded.ValidateWebhookURL("http://127.0.0.1:9000/hook"); err == nil {
		t.Fatal("loopback target should be rejected by default")
	}
	if err := guarded.ValidateWebhookURL("http://192.168.1.10/hook"); err == nil {
		t.Fatal("RFC1918 target should be rejected by default")
	}
	if err := guarded.ValidateWebhookURL("http://169.254.169.254/latest/meta-data"); err == nil {
		t.Fatal("link-local metadata target should be rejected by default")
	}
	if err := permissive.ValidateWebhookURL("http://192.168.1.10/hook"); err != nil {
		t.Fatalf("private target should be allowed when opted in: %v", err)
	}
}

func TestIsRestrictedIP(t *testing.T) {
	restricted := []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.1", "169.254.169.254", "100.64.0.1", "::1", "0.0.0.0"}
	for _, raw := range restricted {
		if !isRestrictedIP(parseTestIP(t, raw)) {
			t.Fatalf("%s should be restricted", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "100.128.0.1", "2606:4700:4700::1111"} {
		if isRestrictedIP(parseTestIP(t, raw)) {
			t.Fatalf("%s should be routable", raw)
		}
	}
}

func parseTestIP(t *testing.T, raw string) net.IP {
	t.Helper()
	ip := net.ParseIP(raw)
	if ip == nil {
		t.Fatalf("could not parse %q as an IP", raw)
	}
	return ip
}

func TestApplyDefaultWebhooks(t *testing.T) {
	alice, bob := uuid.New(), uuid.New()
	defaults := map[uuid.UUID]string{alice: "https://hooks.example.com/alice"}

	rules := []models.AlertRule{
		{Name: "inherits", UserID: alice, WebhookURL: ""},
		{Name: "keeps its own", UserID: alice, WebhookURL: "https://hooks.example.com/override"},
		// Whitespace is not a webhook; the rule form trims, but a value written
		// straight to the API would otherwise suppress the default silently.
		{Name: "blank is not a value", UserID: alice, WebhookURL: "   "},
		// Bob has no default, so his empty rule must stay empty rather than
		// picking up another user's destination.
		{Name: "no default for owner", UserID: bob, WebhookURL: ""},
	}
	ApplyDefaultWebhooks(rules, defaults)

	want := []string{
		"https://hooks.example.com/alice",
		"https://hooks.example.com/override",
		"https://hooks.example.com/alice",
		"",
	}
	for i, w := range want {
		if rules[i].WebhookURL != w {
			t.Errorf("rule %q: webhook = %q, want %q", rules[i].Name, rules[i].WebhookURL, w)
		}
	}
}

func TestApplyDefaultWebhooksNoDefaults(t *testing.T) {
	owner := uuid.New()
	rules := []models.AlertRule{{UserID: owner, WebhookURL: ""}}
	ApplyDefaultWebhooks(rules, nil)
	if rules[0].WebhookURL != "" {
		t.Errorf("webhook = %q, want empty", rules[0].WebhookURL)
	}
}

func TestFormatAlertMessageOfflineNamesTheCause(t *testing.T) {
	offlineRule := rule(models.AlertMetricOffline, ">", 0, 300)

	// With no recorded cause the message is unchanged: an offline alert that
	// predates any classified failure must not gain a dangling separator.
	plain := FormatAlertMessage(offlineRule, snapshot(nil), 480)
	if strings.Contains(plain, "—") {
		t.Errorf("message with no cause should not carry a separator: %q", plain)
	}

	withAuth := FormatAlertMessage(offlineRule, snapshot(func(s *models.AlertSnapshot) {
		s.LastErrorKind = string(PollErrorAuth)
	}), 480)
	for _, want := range []string{"web-01", "8m", "SSH authentication was rejected"} {
		if !strings.Contains(withAuth, want) {
			t.Errorf("message %q missing %q", withAuth, want)
		}
	}

	// `other` has no one-clause phrasing, so it adds nothing rather than
	// padding the alert with a word that carries no information.
	other := FormatAlertMessage(offlineRule, snapshot(func(s *models.AlertSnapshot) {
		s.LastErrorKind = string(PollErrorOther)
	}), 480)
	if other != plain {
		t.Errorf("an unclassifiable cause should read the same as none:\n got  %q\n want %q", other, plain)
	}

	// A threshold alert's cause is its threshold; a stale poll error must not
	// leak into one.
	cpu := FormatAlertMessage(rule(models.AlertMetricCPU, ">", 80, 300), snapshot(func(s *models.AlertSnapshot) {
		s.LastErrorKind = string(PollErrorAuth)
	}), 91.5)
	if strings.Contains(cpu, "authentication") {
		t.Errorf("threshold message should not carry a poll error: %q", cpu)
	}
}
