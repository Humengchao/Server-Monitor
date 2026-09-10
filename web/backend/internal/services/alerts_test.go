package services

import (
	"context"
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

func TestValidateTargetPinsResolvedIPs(t *testing.T) {
	n := NewWebhookNotifier(false)
	n.lookupIP = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("93.184.216.35")}, nil
	}
	ips, err := n.validateTarget("https://hook.example/notify")
	if err != nil {
		t.Fatalf("validateTarget() unexpected error: %v", err)
	}
	if len(ips) != 2 || ips[0].String() != "93.184.216.34" {
		t.Fatalf("validateTarget() IPs = %v, want the two resolved addresses", ips)
	}

	// A stub resolver keeps this hermetic: no external DNS needed to prove a
	// private answer is still rejected (and therefore never pinned).
	n.lookupIP = func(host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("10.0.0.5")}, nil
	}
	if _, err := n.validateTarget("https://hook.example/notify"); err == nil {
		t.Fatal("validateTarget() accepted a host resolving to a private address")
	}
}

func TestDialPinnedNeverResolvesHostname(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			conn.Close()
		}
	}()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	// A .invalid name can never resolve (RFC 2606), so a successful connection
	// proves the dialer used the pinned IP with the URL's port, not DNS.
	conn, err := dialPinned(context.Background(), &net.Dialer{Timeout: 2 * time.Second}, "tcp",
		net.JoinHostPort("rebinding.invalid", port), []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("dialPinned() unexpected error: %v", err)
	}
	conn.Close()

	// An unreachable pinned address must fail rather than fall back to DNS.
	if _, err := dialPinned(context.Background(), &net.Dialer{Timeout: time.Second}, "tcp",
		net.JoinHostPort("rebinding.invalid", port), []net.IP{net.ParseIP("127.0.0.2")}); err == nil {
		t.Fatal("dialPinned() succeeded for a pinned IP with nothing listening")
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
