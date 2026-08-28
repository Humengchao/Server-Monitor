package handlers

import (
	"testing"
	"time"

	"server-monitor/internal/models"
)

func day(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("bad test timestamp %q: %v", value, err)
	}
	return parsed
}

func TestBuildDailyStripFillsMissingDays(t *testing.T) {
	start := day(t, "2026-03-01T00:00:00Z")
	end := day(t, "2026-03-04T00:00:00Z")
	created := day(t, "2026-01-01T00:00:00Z")

	// Only two of the three days produced rows. The absent day is a full
	// outage, and must still appear in the strip.
	daily := []models.DailyAvailability{
		{Day: day(t, "2026-03-01T00:00:00Z"), Observed: 96},
		{Day: day(t, "2026-03-03T00:00:00Z"), Observed: 48},
	}

	strip := buildDailyStrip(daily, start, end, created)
	if len(strip) != 3 {
		t.Fatalf("got %d days, want 3: %+v", len(strip), strip)
	}
	if strip[0].Day != "2026-03-01" || strip[0].Percent != 100 {
		t.Errorf("day 1 = %+v, want 100%%", strip[0])
	}
	if strip[1].Day != "2026-03-02" || strip[1].Percent != 0 || strip[1].NoData {
		t.Errorf("day 2 = %+v, want a 0%% day that is not flagged no-data", strip[1])
	}
	if strip[1].Expected != 96 {
		t.Errorf("day 2 expected = %d, want a full 96 quarter-hours", strip[1].Expected)
	}
	if strip[2].Percent != 50 {
		t.Errorf("day 3 = %+v, want 50%%", strip[2])
	}
}

func TestBuildDailyStripClipsPartialEdges(t *testing.T) {
	// Window starts mid-day and ends mid-day; neither edge day should be scored
	// against a full day of buckets.
	start := day(t, "2026-03-01T18:00:00Z")
	end := day(t, "2026-03-03T06:00:00Z")
	created := day(t, "2026-01-01T00:00:00Z")

	daily := []models.DailyAvailability{
		{Day: day(t, "2026-03-01T00:00:00Z"), Observed: 24},
		{Day: day(t, "2026-03-02T00:00:00Z"), Observed: 96},
		{Day: day(t, "2026-03-03T00:00:00Z"), Observed: 24},
	}
	strip := buildDailyStrip(daily, start, end, created)
	if len(strip) != 3 {
		t.Fatalf("got %d days, want 3", len(strip))
	}
	// 18:00 -> 24:00 is six hours, i.e. 24 quarter-hour buckets.
	if strip[0].Expected != 24 || strip[0].Percent != 100 {
		t.Errorf("first partial day = %+v, want expected=24 percent=100", strip[0])
	}
	if strip[1].Expected != 96 || strip[1].Percent != 100 {
		t.Errorf("middle full day = %+v, want expected=96 percent=100", strip[1])
	}
	// 00:00 -> 06:00 is another 24 buckets.
	if strip[2].Expected != 24 || strip[2].Percent != 100 {
		t.Errorf("last partial day = %+v, want expected=24 percent=100", strip[2])
	}
}

func TestBuildDailyStripRespectsCreationDate(t *testing.T) {
	// A server created halfway through the second day must not be blamed for
	// the time before it existed.
	start := day(t, "2026-03-01T00:00:00Z")
	end := day(t, "2026-03-03T00:00:00Z")
	created := day(t, "2026-03-02T12:00:00Z")

	daily := []models.DailyAvailability{
		{Day: day(t, "2026-03-02T00:00:00Z"), Observed: 48},
	}
	strip := buildDailyStrip(daily, start, end, created)
	if len(strip) != 2 {
		t.Fatalf("got %d days, want 2", len(strip))
	}
	// The whole first day predates the server: nothing was expected, so it is
	// flagged rather than shown as a 0% outage.
	if strip[0].Expected != 0 || !strip[0].NoData {
		t.Errorf("pre-creation day = %+v, want expected=0 and no_data", strip[0])
	}
	// Day two only counts from 12:00, i.e. 48 buckets — a full score.
	if strip[1].Expected != 48 || strip[1].Percent != 100 {
		t.Errorf("creation day = %+v, want expected=48 percent=100", strip[1])
	}
}

func TestWindowBoundsMarksPartialWindows(t *testing.T) {
	now := day(t, "2026-03-31T12:00:00Z")

	// A three-day-old server: 24h is complete, 7d and 30d are partial.
	windows := windowBounds(now, now.Add(-3*24*time.Hour))
	byKey := map[string]UptimeWindowResult{}
	for _, w := range windows {
		byKey[w.Window] = w
	}
	if byKey["24h"].Partial {
		t.Error("24h should be complete for a 3-day-old server")
	}
	if !byKey["7d"].Partial || !byKey["30d"].Partial {
		t.Error("7d and 30d should be partial for a 3-day-old server")
	}
	// 24h at one-minute buckets, minus the rollup lag exclusion.
	if byKey["24h"].Expected != 1440 {
		t.Errorf("24h expected = %d, want 1440", byKey["24h"].Expected)
	}
	// Three days of quarter hours would be 288, but the window ends at the
	// fifteen-minute tier's last closed boundary (11:45, not 11:58), so the
	// still-open bucket is excluded from the expectation just as it is from the
	// query. One short is the correct answer, not an off-by-one.
	if byKey["30d"].Expected != 287 {
		t.Errorf("30d expected = %d, want 287 (3 days less the open quarter hour)", byKey["30d"].Expected)
	}
	// A complete window keeps its exact bucket count, because both ends shift
	// by the lag together.
	complete := windowBounds(now, now.Add(-90*24*time.Hour))
	for _, w := range complete {
		if w.Partial {
			t.Errorf("%s should be complete for a 90-day-old server", w.Window)
		}
	}
}
