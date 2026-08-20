package services

import (
	"errors"
	"testing"
	"time"

	"server-monitor/internal/models"

	"github.com/google/uuid"
)

func TestParsePingAverage(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{"iputils", "rtt min/avg/max/mdev = 31.204/32.684/34.102/1.185 ms", 33},
		{"busybox", "round-trip min/avg/max = 0.451/0.728/1.005 ms", 1},
		{"unavailable", "3 packets transmitted, 0 received, 100% packet loss", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parsePingAverage(test.output); got != test.want {
				t.Fatalf("parsePingAverage() = %d, want %d", got, test.want)
			}
		})
	}
}

func TestPollFailureDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: 15 * time.Second},
		{attempt: 0, want: 15 * time.Second},
		{attempt: 1, want: 15 * time.Second},
		{attempt: 2, want: 30 * time.Second},
		{attempt: 3, want: time.Minute},
		{attempt: 6, want: 8 * time.Minute},
		{attempt: 7, want: 15 * time.Minute},
		{attempt: 100, want: 15 * time.Minute},
	}

	for _, test := range tests {
		if got := pollFailureDelay(test.attempt); got != test.want {
			t.Errorf("pollFailureDelay(%d) = %s, want %s", test.attempt, got, test.want)
		}
	}
}

func TestSecurityFailureDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 10 * time.Minute},
		{attempt: 2, want: 20 * time.Minute},
		{attempt: 3, want: 40 * time.Minute},
		{attempt: 4, want: time.Hour},
		{attempt: 100, want: time.Hour},
	}
	for _, test := range tests {
		if got := securityFailureDelay(test.attempt); got != test.want {
			t.Errorf("securityFailureDelay(%d) = %s, want %s", test.attempt, got, test.want)
		}
	}
}

func TestCollectorPollFailureState(t *testing.T) {
	collector := NewCollector(nil, time.Second)
	serverID := uuid.New()
	fingerprint := [32]byte{1}
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)

	if !collector.beginPollAt(serverID, fingerprint, now) {
		t.Fatal("first beginPollAt() = false, want true")
	}
	if collector.beginPollAt(serverID, fingerprint, now) {
		t.Fatal("beginPollAt() while in flight = true, want false")
	}
	collector.endPoll(serverID)

	firstDelay := collector.recordPollFailure(serverID, fingerprint, errors.New("dial failed"), now)
	if firstDelay != pollFailureBase {
		t.Fatalf("first recordPollFailure() delay = %s, want %s", firstDelay, pollFailureBase)
	}
	firstState, ok := collector.failures[serverID]
	if !ok {
		t.Fatal("recordPollFailure() did not store failure state")
	}
	if firstState.attempts != 1 {
		t.Fatalf("first failure attempts = %d, want 1", firstState.attempts)
	}
	if want := now.Add(firstDelay); !firstState.retryAt.Equal(want) {
		t.Fatalf("first retryAt = %s, want %s", firstState.retryAt, want)
	}
	if collector.beginPollAt(serverID, fingerprint, firstState.retryAt.Add(-time.Nanosecond)) {
		t.Fatal("beginPollAt() before retry deadline = true, want false")
	}
	if !collector.beginPollAt(serverID, fingerprint, firstState.retryAt) {
		t.Fatal("beginPollAt() at retry deadline = false, want true")
	}
	collector.endPoll(serverID)

	secondNow := firstState.retryAt
	secondDelay := collector.recordPollFailure(serverID, fingerprint, errors.New("dial failed"), secondNow)
	if secondDelay != 2*pollFailureBase {
		t.Fatalf("second recordPollFailure() delay = %s, want %s", secondDelay, 2*pollFailureBase)
	}
	if got := collector.failures[serverID].attempts; got != 2 {
		t.Fatalf("second failure attempts = %d, want 2", got)
	}

	collector.clearPollFailure(serverID)
	if _, ok := collector.failures[serverID]; ok {
		t.Fatal("clearPollFailure() left failure state behind")
	}
	if !collector.beginPollAt(serverID, fingerprint, secondNow) {
		t.Fatal("beginPollAt() after clearPollFailure() = false, want true")
	}
	collector.endPoll(serverID)
}

func TestCollectorConfigChangeClearsBackoff(t *testing.T) {
	collector := NewCollector(nil, time.Second)
	serverID := uuid.New()
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	oldFingerprint := [32]byte{1}
	newFingerprint := [32]byte{2}

	collector.recordPollFailure(serverID, oldFingerprint, errors.New("dial failed"), now)
	if collector.beginPollAt(serverID, oldFingerprint, now.Add(time.Second)) {
		t.Fatal("unchanged config bypassed active backoff")
	}
	if !collector.beginPollAt(serverID, newFingerprint, now.Add(time.Second)) {
		t.Fatal("changed config did not clear active backoff")
	}
	collector.endPoll(serverID)
	if _, exists := collector.failures[serverID]; exists {
		t.Fatal("changed config left stale failure state")
	}
}

func TestCollectorSSHAuthenticationFailureUsesCircuitBreaker(t *testing.T) {
	collector := NewCollector(nil, time.Second)
	serverID := uuid.New()
	fingerprint := [32]byte{1}
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	authErr := errors.New("ssh handshake: ssh: unable to authenticate")

	if got := collector.recordPollFailure(serverID, fingerprint, authErr, now); got != securityFailureBase {
		t.Fatalf("authentication failure delay = %s, want %s", got, securityFailureBase)
	}
	// Once authentication trouble is detected, a subsequent connection reset
	// remains on the conservative circuit-breaker schedule.
	resetErr := errors.New("ssh handshake: read: connection reset by peer")
	if got := collector.recordPollFailure(serverID, fingerprint, resetErr, now.Add(securityFailureBase)); got != 2*securityFailureBase {
		t.Fatalf("connection reset delay = %s, want %s", got, 2*securityFailureBase)
	}
	if !collector.failures[serverID].securityFailure {
		t.Fatal("authentication failure did not set sticky security-failure state")
	}
}

func TestCollectorConnectionResetAloneUsesNormalBackoff(t *testing.T) {
	collector := NewCollector(nil, time.Second)
	serverID := uuid.New()
	fingerprint := [32]byte{1}
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	resetErr := errors.New("ssh handshake: read: connection reset by peer")

	if got := collector.recordPollFailure(serverID, fingerprint, resetErr, now); got != pollFailureBase {
		t.Fatalf("first connection reset delay = %s, want %s", got, pollFailureBase)
	}
	if collector.failures[serverID].securityFailure {
		t.Fatal("connection reset without a prior auth failure enabled security circuit breaker")
	}
}

func TestCollectorFirstAuthFailureResetsGenericAttempts(t *testing.T) {
	collector := NewCollector(nil, time.Second)
	serverID := uuid.New()
	fingerprint := [32]byte{1}
	now := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	collector.recordPollFailure(serverID, fingerprint, errors.New("dial failed"), now)
	collector.recordPollFailure(serverID, fingerprint, errors.New("dial failed again"), now.Add(time.Minute))

	authErr := errors.New("ssh handshake: ssh: unable to authenticate")
	if got := collector.recordPollFailure(serverID, fingerprint, authErr, now.Add(2*time.Minute)); got != securityFailureBase {
		t.Fatalf("first auth failure after generic failures = %s, want %s", got, securityFailureBase)
	}
	if got := collector.failures[serverID].attempts; got != 1 {
		t.Fatalf("security attempt counter = %d, want 1", got)
	}
}

func TestSplitLinuxSectionsAndParse(t *testing.T) {
	sep := linuxSectionSeparator + "\n"
	out := "cpu  100 0 100 700 100 0 0 0\ncpu0 50 0 50 350 50 0 0 0\n" + sep +
		"cpu  150 0 150 800 100 0 0 0\ncpu0 75 0 75 400 50 0 0 0\n" + sep +
		"MemTotal:       2048 kB\nMemFree:         512 kB\nBuffers:         128 kB\nCached:          128 kB\n" + sep +
		"0.50 0.25 0.10 1/234 5678\n" + sep +
		"Inter-|   Receive\n face |bytes    packets\n    lo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n  eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n" + sep +
		"   8       0 sda 100 0 2048 0 200 0 4096 0 0 0 0\n   8       1 sda1 50 0 1024 0 100 0 2048 0 0 0 0\n" + sep +
		"12345.67 23456.78\n" + sep +
		"4\n" + sep +
		"Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/vda1 100000 25000 75000 25% /\n" + sep +
		"24.0.7\n"

	sections := splitLinuxSections(out, linuxSectionCount)
	if len(sections) < linuxSectionCount {
		t.Fatalf("splitLinuxSections() returned %d sections, want at least %d", len(sections), linuxSectionCount)
	}

	if got := cpuPercentFromSamples(sections[linuxSectionStatFirst], sections[linuxSectionStatSecond]); got != 50 {
		t.Errorf("cpuPercentFromSamples() = %v, want 50", got)
	}
	used, total := parseMemInfo(sections[linuxSectionMemInfo])
	if used != 1280*1024 || total != 2048*1024 {
		t.Errorf("parseMemInfo() = (%d, %d), want (%d, %d)", used, total, 1280*1024, 2048*1024)
	}
	l1, l5, l15 := parseLoadAvg(sections[linuxSectionLoadAvg])
	if l1 != 0.5 || l5 != 0.25 || l15 != 0.1 {
		t.Errorf("parseLoadAvg() = (%v, %v, %v), want (0.5, 0.25, 0.1)", l1, l5, l15)
	}
	rx, tx := parseNetDev(sections[linuxSectionNetDev])
	if rx != 1000 || tx != 2000 {
		t.Errorf("parseNetDev() = (%d, %d), want (1000, 2000) excluding lo", rx, tx)
	}
	diskRx, diskTx := parseDiskStats(sections[linuxSectionDiskStats])
	if diskRx != 2048*512 || diskTx != 4096*512 {
		t.Errorf("parseDiskStats() = (%d, %d), want (%d, %d) excluding partitions", diskRx, diskTx, 2048*512, 4096*512)
	}
	if got := parseUptime(sections[linuxSectionUptime]); got != 12345 {
		t.Errorf("parseUptime() = %d, want 12345", got)
	}
	if got := parseNproc(sections[linuxSectionNproc]); got != 4 {
		t.Errorf("parseNproc() = %d, want 4", got)
	}
	diskUsed, diskTotal := parseDiskUsage(sections[linuxSectionDiskUsage])
	if diskUsed != 25000 || diskTotal != 100000 {
		t.Errorf("parseDiskUsage() = (%d, %d), want (25000, 100000)", diskUsed, diskTotal)
	}
	if got := parseDockerVersion(sections[linuxSectionDocker]); got != "24.0.7" {
		t.Errorf("parseDockerVersion() = %q, want \"24.0.7\"", got)
	}
}

func TestSplitLinuxSectionsTruncated(t *testing.T) {
	out := "cpu  1 2 3 4 5 6 7 8\n" + linuxSectionSeparator + "\ncpu  1 2 3 4 5 6 7 8\n"
	sections := splitLinuxSections(out, linuxSectionCount)
	if len(sections) != linuxSectionCount {
		t.Fatalf("splitLinuxSections() returned %d sections, want %d", len(sections), linuxSectionCount)
	}
	for i := linuxSectionMemInfo; i < linuxSectionCount; i++ {
		if sections[i] != "" {
			t.Fatalf("section %d = %q, want empty for truncated output", i, sections[i])
		}
	}
	if used, total := parseMemInfo(sections[linuxSectionMemInfo]); used != 0 || total != 0 {
		t.Fatalf("parseMemInfo(empty) = (%d, %d), want zeros", used, total)
	}
}

func TestParseDiskUsageWrappedDeviceName(t *testing.T) {
	out := "Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/mapper/very-long-volume-name\n 100000 25000 75000 25% /\n"
	used, total := parseDiskUsage(out)
	if used != 25000 || total != 100000 {
		t.Fatalf("parseDiskUsage() = (%d, %d), want (25000, 100000)", used, total)
	}
}

func TestParseDockerVersionRejectsNoise(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"version", "24.0.7\n", "24.0.7"},
		{"empty", "", ""},
		{"error text", "Cannot connect to the Docker daemon at unix:///var/run/docker.sock\n", ""},
		{"multiline", "warning\n24.0.7\n", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseDockerVersion(test.in); got != test.want {
				t.Fatalf("parseDockerVersion(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestServerPollFingerprintChangesWithCredentials(t *testing.T) {
	server := &models.Server{
		Host:        "example.com",
		Port:        22,
		SSHUsername: "root",
		SSHPassword: "old password",
		ServerType:  "linux",
	}
	before := serverPollFingerprint(server)
	server.SSHPassword = "new password"
	after := serverPollFingerprint(server)
	if before == after {
		t.Fatal("serverPollFingerprint() did not change after password update")
	}
}
