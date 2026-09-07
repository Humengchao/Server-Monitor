package services

import (
	"testing"
	"time"
)

func TestAvailabilityPercent(t *testing.T) {
	tests := []struct {
		observed, expected int
		want               float64
	}{
		{1440, 1440, 100},
		{1439, 1440, 99.93},
		{720, 1440, 50},
		{0, 1440, 0},
		// A rollup overlap can leave one bucket more than the window nominally
		// holds; that must read as 100%, never 100.07%.
		{1441, 1440, 100},
		// No elapsed window means nothing to report, not a division by zero.
		{0, 0, 0},
		{5, 0, 0},
	}
	for _, tc := range tests {
		if got := AvailabilityPercent(tc.observed, tc.expected); got != tc.want {
			t.Errorf("AvailabilityPercent(%d, %d) = %v, want %v", tc.observed, tc.expected, got, tc.want)
		}
	}
}

func TestExpectedBuckets(t *testing.T) {
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	if got := ExpectedBuckets(start, start.Add(24*time.Hour), 60); got != 1440 {
		t.Errorf("24h in minutes = %d, want 1440", got)
	}
	if got := ExpectedBuckets(start, start.Add(30*24*time.Hour), 900); got != 2880 {
		t.Errorf("30d in 15-minute buckets = %d, want 2880", got)
	}
	// An end at or before the start cannot produce a negative expectation.
	if got := ExpectedBuckets(start, start, 60); got != 0 {
		t.Errorf("zero-length window = %d, want 0", got)
	}
	if got := ExpectedBuckets(start, start.Add(-time.Hour), 60); got != 0 {
		t.Errorf("inverted window = %d, want 0", got)
	}
	if got := ExpectedBuckets(start, start.Add(time.Hour), 0); got != 0 {
		t.Errorf("zero bucket size = %d, want 0", got)
	}
}

func TestExpectedBucketsCountsAlignmentBoundaries(t *testing.T) {
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	// Midnight to 20:27:28 contains 82 quarter-hour boundaries (00:00 through
	// 20:15). Dividing the 73648-second span by 900 floors to 81, which is the
	// bug this test pins: a real query over that range can return 82 rows.
	if got := ExpectedBuckets(day, day.Add(20*time.Hour+27*time.Minute+28*time.Second), 900); got != 82 {
		t.Errorf("partial trailing day = %d, want 82", got)
	}
	// The mirror image: an unaligned start to midnight. 20:30 through 23:45 is
	// 14 boundaries — the bucket straddling the start belongs to the day before.
	if got := ExpectedBuckets(day.Add(20*time.Hour+27*time.Minute+28*time.Second), day.Add(24*time.Hour), 900); got != 14 {
		t.Errorf("partial leading day = %d, want 14", got)
	}
	// Splitting a day at an arbitrary instant must not invent or lose a bucket,
	// which is what keeps the daily strip's sum equal to the window headline.
	split := day.Add(20*time.Hour + 27*time.Minute + 28*time.Second)
	lead := ExpectedBuckets(day, split, 900)
	tail := ExpectedBuckets(split, day.Add(24*time.Hour), 900)
	if lead+tail != 96 {
		t.Errorf("split day = %d + %d = %d, want 96", lead, tail, lead+tail)
	}
	// A range that lies strictly between two boundaries expects nothing: the
	// preceding bucket's row is stamped before the range starts, so a query over
	// it returns no rows and there is nothing to hold the server to.
	if got := ExpectedBuckets(day.Add(3*time.Minute), day.Add(7*time.Minute), 900); got != 0 {
		t.Errorf("sub-bucket range = %d, want 0", got)
	}
}

func TestTierEndExcludesTheOpenBucket(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)

	// The one-minute tier only needs the lag: two minutes back is already a
	// closed, consolidated minute.
	if got := TierEnd(now, 60); !got.Equal(now.Add(-2 * time.Minute)) {
		t.Errorf("1m tier end = %s, want 11:58", got.Format(time.RFC3339))
	}
	// The fifteen-minute tier snaps back to 11:45. Stopping at 11:58 would reach
	// into the quarter hour that the rollup will not write until 12:00, and every
	// healthy host would report one bucket short of 100% forever.
	if got := TierEnd(now, 900); !got.Equal(time.Date(2026, 3, 31, 11, 45, 0, 0, time.UTC)) {
		t.Errorf("15m tier end = %s, want 11:45", got.Format(time.RFC3339))
	}
	// A perfectly-reporting host over a complete window scores exactly 100%:
	// both ends land on boundaries, so the count is a whole number of buckets.
	end := TierEnd(now, 900)
	if got := ExpectedBuckets(end.Add(-30*24*time.Hour), end, 900); got != 2880 {
		t.Errorf("30d from a snapped end = %d, want exactly 2880", got)
	}
	if got := AvailabilityPercent(2880, 2880); got != 100 {
		t.Errorf("full census = %v%%, want 100", got)
	}
}

func TestWindowStartClampsToCreation(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)

	// A long-lived server uses the full window.
	old := now.Add(-90 * 24 * time.Hour)
	start, partial := WindowStart(now, 30*24*time.Hour, old)
	if partial {
		t.Error("a server older than the window should not be marked partial")
	}
	if !start.Equal(now.Add(-30 * 24 * time.Hour)) {
		t.Errorf("start = %v, want exactly 30 days back", start)
	}

	// A server added two hours ago must be measured from its creation, or it
	// would report ~0.3% availability over 30 days.
	fresh := now.Add(-2 * time.Hour)
	start, partial = WindowStart(now, 30*24*time.Hour, fresh)
	if !partial {
		t.Error("a server younger than the window should be marked partial")
	}
	if !start.Equal(fresh) {
		t.Errorf("start = %v, want the creation time %v", start, fresh)
	}
	if got := ExpectedBuckets(start, now, 900); got != 8 {
		t.Errorf("expected buckets for a 2h-old server = %d, want 8", got)
	}
}

func TestBuildOutages(t *testing.T) {
	base := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	windowEnd := base.Add(time.Hour)

	gaps := []MetricGap{
		{Before: base.Add(5 * time.Minute), After: base.Add(11 * time.Minute)},
		{Before: base.Add(30 * time.Minute), After: base.Add(33 * time.Minute)},
	}
	// Last sample is recent, so there is no trailing episode.
	outages := BuildOutages(gaps, base.Add(59*time.Minute), windowEnd)
	if len(outages) != 2 {
		t.Fatalf("got %d outages, want 2", len(outages))
	}
	if outages[0].Seconds != 360 {
		t.Errorf("first outage = %ds, want 360", outages[0].Seconds)
	}
	if outages[0].Ongoing || outages[1].Ongoing {
		t.Error("recovered outages must not be marked ongoing")
	}
}

func TestBuildOutagesDetectsOngoingDowntime(t *testing.T) {
	base := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	windowEnd := base.Add(time.Hour)

	// The host stopped reporting 40 minutes ago. There is no later bucket to
	// pair it with, so only the trailing check can surface this.
	outages := BuildOutages(nil, base.Add(20*time.Minute), windowEnd)
	if len(outages) != 1 {
		t.Fatalf("got %d outages, want 1 trailing episode", len(outages))
	}
	if !outages[0].Ongoing {
		t.Error("the trailing episode should be marked ongoing")
	}
	if outages[0].Seconds != 2400 {
		t.Errorf("ongoing outage = %ds, want 2400", outages[0].Seconds)
	}

	// A fresh last sample produces nothing.
	if got := BuildOutages(nil, windowEnd.Add(-30*time.Second), windowEnd); len(got) != 0 {
		t.Errorf("got %d outages for a healthy host, want 0", len(got))
	}

	// A server that never reported has no last sample; the gap query has
	// nothing either, so there is no episode to synthesise.
	if got := BuildOutages(nil, time.Time{}, windowEnd); len(got) != 0 {
		t.Errorf("got %d outages for a server with no samples, want 0", len(got))
	}
}

func TestUptimeWindowsCoverTheDocumentedTiers(t *testing.T) {
	// The 30-day window must read the fifteen-minute tier: the one-minute tier
	// is only retained for 30 days, so a 30-day query against it would clip.
	byKey := map[string]UptimeWindowSpec{}
	for _, w := range UptimeWindows {
		byKey[w.Key] = w
	}
	if byKey["24h"].BucketSeconds != 60 || byKey["7d"].BucketSeconds != 60 {
		t.Error("24h and 7d should use the one-minute tier")
	}
	if byKey["30d"].BucketSeconds != 900 {
		t.Error("30d should use the fifteen-minute tier")
	}
}
