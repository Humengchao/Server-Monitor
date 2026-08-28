package handlers

import (
	"strings"
	"testing"

	"server-monitor/internal/services"

	"github.com/google/uuid"
)

func TestDedupeIDs(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()

	got, err := dedupeIDs([]uuid.UUID{a, b, a, c, b}, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 || got[0] != a || got[1] != b || got[2] != c {
		t.Fatalf("got %v, want [a b c] in first-seen order", got)
	}

	if _, err := dedupeIDs(nil, 0); err == nil {
		t.Error("an empty selection should be rejected")
	}

	// The limit applies after deduplication, so re-sending the same ID twice
	// does not eat into the budget.
	if _, err := dedupeIDs([]uuid.UUID{a, a}, 1); err != nil {
		t.Errorf("duplicates should collapse below the limit: %v", err)
	}
	if _, err := dedupeIDs([]uuid.UUID{a, b}, 1); err == nil {
		t.Error("a selection over the limit should be rejected")
	}
}

func TestNormalizeBatchCommand(t *testing.T) {
	got, err := normalizeBatchCommand("  uptime  ")
	if err != nil || got != "uptime" {
		t.Fatalf("normalizeBatchCommand = (%q, %v), want (\"uptime\", nil)", got, err)
	}

	// Tabs are legitimate inside a command (awk field separators, etc.).
	if _, err := normalizeBatchCommand("awk -F'\t' '{print $1}' /tmp/x"); err != nil {
		t.Errorf("tabs should be allowed: %v", err)
	}

	for name, input := range map[string]string{
		"empty":         "   ",
		"newline":       "uptime\nrm -rf /",
		"carriage":      "uptime\rrm -rf /",
		"null byte":     "uptime\x00",
		"escape sequue": "uptime\x1b[2J",
		"too long":      strings.Repeat("a", services.BatchMaxCommandLength+1),
	} {
		if _, err := normalizeBatchCommand(input); err == nil {
			t.Errorf("%s should be rejected", name)
		}
	}
}
