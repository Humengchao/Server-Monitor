package handlers

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// fleetUptimeCacheTTL bounds how often the census runs per user. Availability
// over 24 hours cannot move meaningfully inside a minute, and the dashboard
// polls far more often than that.
const fleetUptimeCacheTTL = 60 * time.Second

type UptimeWindowResult struct {
	Window   string  `json:"window"`
	Percent  float64 `json:"percent"`
	Observed int     `json:"observed_buckets"`
	Expected int     `json:"expected_buckets"`
	// Partial marks a window longer than the server has existed, so the figure
	// covers less time than its label suggests.
	Partial bool `json:"partial"`
	// NoData marks a window that expected nothing at all — a server added
	// minutes ago has not lived through a whole bucket of the coarser tiers yet.
	// Without it the percentage reads 0.00%, which looks like a total outage
	// rather than "ask again later".
	NoData bool `json:"no_data"`
}

type ServerUptimeResult struct {
	ServerID uuid.UUID            `json:"server_id"`
	Windows  []UptimeWindowResult `json:"windows"`
}

type uptimeCacheEntry struct {
	payload gin.H
	expires time.Time
}

type UptimeHandler struct {
	mu    sync.Mutex
	cache map[uuid.UUID]uptimeCacheEntry
}

func NewUptimeHandler() *UptimeHandler {
	return &UptimeHandler{cache: make(map[uuid.UUID]uptimeCacheEntry)}
}

// windowBounds computes each window's clamped start and the bucket expectation
// for a server of a given age. Each window ends at its own tier's boundary, the
// same instant the census query is bounded by.
func windowBounds(now, createdAt time.Time) []UptimeWindowResult {
	results := make([]UptimeWindowResult, 0, len(services.UptimeWindows))
	for _, spec := range services.UptimeWindows {
		end := services.TierEnd(now, spec.BucketSeconds)
		start, partial := services.WindowStart(end, spec.Span, createdAt)
		expected := services.ExpectedBuckets(start, end, spec.BucketSeconds)
		results = append(results, UptimeWindowResult{
			Window:   spec.Key,
			Expected: expected,
			Partial:  partial,
			NoData:   expected == 0,
		})
	}
	return results
}

// Fleet returns observed availability per server for every reporting window.
func (h *UptimeHandler) Fleet(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)

	h.mu.Lock()
	if entry, ok := h.cache[userID]; ok && time.Now().Before(entry.expires) {
		payload := entry.payload
		h.mu.Unlock()
		c.JSON(http.StatusOK, payload)
		return
	}
	h.mu.Unlock()

	now := time.Now().UTC()
	end1m := services.TierEnd(now, 60)
	end15m := services.TierEnd(now, models.DailyStripBucketSeconds)
	db := c.MustGet("db").(*models.DB)
	counts, err := models.GetFleetUptimeCounts(db.Raw, userID, models.UptimeCensusRange{
		Since24h: end1m.Add(-24 * time.Hour),
		Since7d:  end1m.Add(-7 * 24 * time.Hour),
		Until1m:  end1m,
		Since30d: end15m.Add(-30 * 24 * time.Hour),
		Until15m: end15m,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load availability"})
		return
	}

	servers := make([]ServerUptimeResult, 0, len(counts))
	for _, item := range counts {
		windows := windowBounds(now, item.CreatedAt)
		observed := map[string]int{
			"24h": item.Minute24h,
			"7d":  item.Minute7d,
			"30d": item.Quarter30d,
		}
		for i := range windows {
			windows[i].Observed = observed[windows[i].Window]
			windows[i].Percent = services.AvailabilityPercent(windows[i].Observed, windows[i].Expected)
		}
		servers = append(servers, ServerUptimeResult{ServerID: item.ServerID, Windows: windows})
	}

	payload := gin.H{
		"servers": servers,
		// Spelled out in the response so a client rendering an SLA badge can
		// caption it honestly rather than implying host-side uptime.
		"basis":        "observed",
		"basis_note":   "Share of expected metric samples that were collected. Panel downtime counts against every server.",
		"generated_at": now,
	}

	h.mu.Lock()
	h.cache[userID] = uptimeCacheEntry{payload: payload, expires: time.Now().Add(fleetUptimeCacheTTL)}
	h.mu.Unlock()

	c.JSON(http.StatusOK, payload)
}

// Detail returns one server's availability plus a daily strip and the outage
// episodes behind it.
func (h *UptimeHandler) Detail(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	days := 30
	if raw := c.Query("days"); raw != "" {
		if parsed, convErr := strconv.Atoi(raw); convErr == nil && parsed >= 1 && parsed <= 30 {
			days = parsed
		}
	}

	db := c.MustGet("db").(*models.DB)
	createdAt, err := models.GetServerCreatedAt(db.Raw, id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	now := time.Now().UTC()
	// The strip reads the fifteen-minute tier and the outage list reads the
	// one-minute tier, so each is bounded by its own tier's edge.
	stripEnd := services.TierEnd(now, models.DailyStripBucketSeconds)
	outageEnd := services.TierEnd(now, 60)
	span := time.Duration(days) * 24 * time.Hour
	start, _ := services.WindowStart(stripEnd, span, createdAt)
	outageStart, _ := services.WindowStart(outageEnd, span, createdAt)

	daily, err := models.GetDailyAvailability(db.Raw, id, start, stripEnd)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load availability"})
		return
	}
	gapRows, err := models.GetMetricGaps(db.Raw, id, outageStart, outageEnd,
		int(services.UptimeOutageGap.Seconds()), services.UptimeMaxOutages)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load outages"})
		return
	}
	lastSample, err := models.GetLastMetricBucket(db.Raw, id, outageEnd)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load availability"})
		return
	}

	gaps := make([]services.MetricGap, 0, len(gapRows))
	for _, row := range gapRows {
		gaps = append(gaps, services.MetricGap{Before: row.Before, After: row.After})
	}
	outages := services.BuildOutages(gaps, lastSample, outageEnd)

	c.JSON(http.StatusOK, gin.H{
		"server_id":    id,
		"since":        start,
		"until":        stripEnd,
		"days":         buildDailyStrip(daily, start, stripEnd, createdAt),
		"outages":      outages,
		"basis":        "observed",
		"generated_at": now,
	})
}

type dailyStripEntry struct {
	Day      string  `json:"day"`
	Percent  float64 `json:"percent"`
	Observed int     `json:"observed_buckets"`
	Expected int     `json:"expected_buckets"`
	// NoData distinguishes "the server did not exist yet" from "it was down all
	// day", which must not look the same in the strip.
	NoData bool `json:"no_data"`
}

// buildDailyStrip fills every calendar day in the range, including days with no
// rows at all — a fully-down day is absent from the query result and would
// otherwise silently vanish from the chart.
func buildDailyStrip(daily []models.DailyAvailability, start, end, createdAt time.Time) []dailyStripEntry {
	observedByDay := make(map[string]int, len(daily))
	for _, item := range daily {
		observedByDay[item.Day.UTC().Format("2006-01-02")] = item.Observed
	}

	entries := make([]dailyStripEntry, 0, 31)
	cursor := start.UTC().Truncate(24 * time.Hour)
	// Strictly Before: when the window ends exactly at midnight, an inclusive
	// bound would emit one extra day covering no time at all.
	for cursor.Before(end.UTC()) {
		dayStart := cursor
		dayEnd := cursor.Add(24 * time.Hour)
		// Clip the first and last days to the window, and to when the server
		// was created, so a partial day is not scored against a whole one.
		effectiveStart := dayStart
		if start.After(effectiveStart) {
			effectiveStart = start
		}
		if createdAt.After(effectiveStart) {
			effectiveStart = createdAt
		}
		effectiveEnd := dayEnd
		if end.Before(effectiveEnd) {
			effectiveEnd = end
		}

		key := dayStart.Format("2006-01-02")
		expected := services.ExpectedBuckets(effectiveStart, effectiveEnd, models.DailyStripBucketSeconds)
		observed := observedByDay[key]
		entries = append(entries, dailyStripEntry{
			Day:      key,
			Observed: observed,
			Expected: expected,
			Percent:  services.AvailabilityPercent(observed, expected),
			NoData:   expected == 0,
		})
		cursor = dayEnd
	}
	return entries
}
