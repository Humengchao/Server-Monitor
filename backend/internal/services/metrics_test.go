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
