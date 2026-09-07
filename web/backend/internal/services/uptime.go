package services

import (
	"math"
	"time"
)

// Availability is derived from how many metric buckets actually landed in a
// window versus how many the collector should have written. That makes it
// *observed* availability: if the panel itself was down, or its database was
// unreachable, every server looks unavailable for that stretch. It measures
// "did we see this host", not the host's own uptime counter.
const (
	// UptimeRollupLag excludes the trailing stretch that the rollup has not
	// consolidated yet. Without it every server would sit at a permanent
	// sub-100% ceiling. It is applied on top of a whole-bucket snap; see
	// TierEnd.
	UptimeRollupLag = 2 * time.Minute
	// UptimeOutageGap is the smallest gap between consecutive buckets counted
	// as an outage. One missing minute is usually a slow poll, not downtime.
	UptimeOutageGap = 2 * time.Minute
	// UptimeMaxOutages bounds the episode list returned per server.
	UptimeMaxOutages = 50
)

// AvailabilityPercent converts observed/expected bucket counts into a
// percentage rounded to two decimals. ExpectedBuckets counts exactly the
// buckets a query can return, so observed should never exceed it; the clamp is
// a guard against a caller pairing counts from two different tiers.
func AvailabilityPercent(observed, expected int) float64 {
	if expected <= 0 {
		return 0
	}
	ratio := float64(observed) / float64(expected) * 100
	if ratio > 100 {
		ratio = 100
	}
	if ratio < 0 {
		ratio = 0
	}
	return math.Round(ratio*100) / 100
}

// UptimeWindowSpec describes one reporting window.
type UptimeWindowSpec struct {
	// Key is the label the API and UI use ("24h", "7d", "30d").
	Key string
	// Span is how far back the window reaches.
	Span time.Duration
	// BucketSeconds is the granularity of the metric tier this window reads,
	// which is what turns a bucket count into an amount of time.
	BucketSeconds int
}

// UptimeWindows are the windows the API reports. The 30-day figure reads the
// fifteen-minute tier: the one-minute tier only retains 30 days and scanning it
// for every server would be 20x the index work for no visible extra precision
// at that timescale.
var UptimeWindows = []UptimeWindowSpec{
	{Key: "24h", Span: 24 * time.Hour, BucketSeconds: 60},
	{Key: "7d", Span: 7 * 24 * time.Hour, BucketSeconds: 60},
	{Key: "30d", Span: 30 * 24 * time.Hour, BucketSeconds: 15 * 60},
}

// ExpectedBuckets counts the rollup buckets that should exist in [start, end).
//
// Rollup rows are wall-clock aligned — the rollup stamps each one
// TO_TIMESTAMP(FLOOR(EXTRACT(EPOCH FROM recorded_at) / width) * width) — so this
// counts alignment boundaries inside the half-open range rather than dividing
// the duration by the bucket width. The distinction only shows up at an edge
// that is not itself aligned, which is every partial day in the daily strip and
// every window clamped to a server's creation time: dividing the duration
// undercounts by one there, and the day then reports more observed buckets than
// expected. A window that has not elapsed yet yields zero rather than a
// negative count.
func ExpectedBuckets(start, end time.Time, bucketSeconds int) int {
	if bucketSeconds <= 0 || !end.After(start) {
		return 0
	}
	width := time.Duration(bucketSeconds) * time.Second
	first, last := ceilBucket(start, width), ceilBucket(end, width)
	if !last.After(first) {
		return 0
	}
	return int(last.Sub(first) / width)
}

// ceilBucket rounds t up to the next bucket boundary, leaving an already-aligned
// time where it is. Truncate measures from Go's zero time, which is an exact
// multiple of both 60s and 900s away from the Unix epoch the rollup floors
// against, so the two agree on where a boundary falls.
func ceilBucket(t time.Time, width time.Duration) time.Time {
	truncated := t.Truncate(width)
	if truncated.Equal(t) {
		return t
	}
	return truncated.Add(width)
}

// TierEnd is the newest instant a census of the given tier may cover.
//
// It snaps back to a bucket boundary after subtracting the rollup lag, which
// excludes the bucket still being filled. A bare lag is not enough: it is sized
// for the one-minute tier, and on the fifteen-minute tier a window ending two
// minutes ago still reaches into a quarter hour whose row will not be written
// for another thirteen. Expecting that row is what pins a permanently healthy
// host just below 100%.
//
// The same instant has to bound the SQL range and the expectation, or the two
// disagree by exactly the bucket at the edge.
func TierEnd(now time.Time, bucketSeconds int) time.Time {
	if bucketSeconds <= 0 {
		return now
	}
	return now.Add(-UptimeRollupLag).Truncate(time.Duration(bucketSeconds) * time.Second)
}

// WindowStart clamps a window's start to when the server was created, so a host
// added an hour ago does not report 4% availability over 30 days.
func WindowStart(now time.Time, span time.Duration, createdAt time.Time) (start time.Time, partial bool) {
	start = now.Add(-span)
	if createdAt.After(start) {
		return createdAt, true
	}
	return start, false
}

// Outage is one contiguous stretch with no metric samples.
type Outage struct {
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	Seconds   int64     `json:"seconds"`
	// Ongoing marks an outage that had not recovered when the window ended.
	Ongoing bool `json:"ongoing"`
}

// MetricGap is a pair of adjacent bucket timestamps far enough apart to count
// as downtime. The database finds these with a window function so the whole
// bucket list never has to travel.
type MetricGap struct {
	Before time.Time
	After  time.Time
}

// BuildOutages turns bucket gaps into outage episodes, and appends a trailing
// episode when the last sample is older than the gap threshold — a host that is
// down *right now* has no "after" bucket to pair with, so the gap query alone
// would never report it.
func BuildOutages(gaps []MetricGap, lastSample time.Time, windowEnd time.Time) []Outage {
	outages := make([]Outage, 0, len(gaps)+1)
	for _, gap := range gaps {
		outages = append(outages, Outage{
			StartedAt: gap.Before,
			EndedAt:   gap.After,
			Seconds:   int64(gap.After.Sub(gap.Before).Seconds()),
		})
	}
	if !lastSample.IsZero() && windowEnd.Sub(lastSample) > UptimeOutageGap {
		outages = append(outages, Outage{
			StartedAt: lastSample,
			EndedAt:   windowEnd,
			Seconds:   int64(windowEnd.Sub(lastSample).Seconds()),
			Ongoing:   true,
		})
	}
	return outages
}
