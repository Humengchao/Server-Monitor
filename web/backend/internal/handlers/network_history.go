package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const networkTrendCacheTTL = 45 * time.Second
const networkTrendCacheLimit = 64

type networkTrendEntry struct {
	done    chan struct{}
	value   models.NetworkTrend
	err     error
	expires time.Time
	cancel  context.CancelFunc
	waiters int
}

// Bound both retained results (each at most 500 buckets) and concurrent SQL.
// Shared work is canceled when its final waiter disconnects; one canceled
// caller does not interrupt another caller that still needs the same result.
type networkTrendCache struct {
	mu      sync.Mutex
	entries map[string]*networkTrendEntry
	slots   chan struct{}
}

func newNetworkTrendCache() *networkTrendCache {
	return &networkTrendCache{entries: make(map[string]*networkTrendEntry), slots: make(chan struct{}, 2)}
}

func (cache *networkTrendCache) get(ctx context.Context, key string, load func(context.Context) (models.NetworkTrend, error)) (models.NetworkTrend, error) {
	if err := ctx.Err(); err != nil {
		return models.NetworkTrend{}, err
	}
	cache.mu.Lock()
	now := time.Now()
	for name, entry := range cache.entries {
		if !entry.expires.IsZero() && !now.Before(entry.expires) {
			delete(cache.entries, name)
		}
	}
	entry := cache.entries[key]
	if entry == nil {
		if len(cache.entries) >= networkTrendCacheLimit {
			var oldestKey string
			var oldest time.Time
			for name, candidate := range cache.entries {
				if !candidate.expires.IsZero() && (oldest.IsZero() || candidate.expires.Before(oldest)) {
					oldestKey, oldest = name, candidate.expires
				}
			}
			if !oldest.IsZero() {
				delete(cache.entries, oldestKey)
			} else {
				cache.mu.Unlock()
				return models.NetworkTrend{}, errors.New("too many pending network history queries")
			}
		}
		task, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		entry = &networkTrendEntry{done: make(chan struct{}), cancel: cancel}
		cache.entries[key] = entry
		go func() {
			defer cancel()
			select {
			case cache.slots <- struct{}{}:
				entry.value, entry.err = load(task)
				<-cache.slots
			case <-task.Done():
				entry.err = task.Err()
			}
			cache.mu.Lock()
			entry.expires = time.Now().Add(networkTrendCacheTTL)
			if entry.err != nil && cache.entries[key] == entry {
				delete(cache.entries, key)
			}
			close(entry.done)
			cache.mu.Unlock()
		}()
	}
	entry.waiters++
	cache.mu.Unlock()
	defer func() {
		cache.mu.Lock()
		entry.waiters--
		if entry.waiters == 0 && entry.expires.IsZero() {
			entry.cancel()
			if cache.entries[key] == entry {
				delete(cache.entries, key)
			}
		}
		cache.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return models.NetworkTrend{}, ctx.Err()
	case <-entry.done:
		return entry.value, entry.err
	}
}

func networkHistoryRange(c *gin.Context) (time.Time, time.Time, bool) {
	until := time.Now().UTC()
	since := until.Add(-time.Hour)
	var err error
	if value := c.Query("since"); value != "" {
		since, err = time.Parse(time.RFC3339Nano, value)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid since"})
			return since, until, false
		}
	}
	if value := c.Query("until"); value != "" {
		until, err = time.Parse(time.RFC3339Nano, value)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid until"})
			return since, until, false
		}
	}
	if !since.Before(until) || until.Sub(since) > 7*24*time.Hour {
		c.JSON(400, gin.H{"error": "history must span at most seven days"})
		return since, until, false
	}
	return since.UTC(), until.UTC(), true
}

func (h *NetworkHandler) loadNetworkTrend(ctx context.Context, db *sql.DB, id, user uuid.UUID, since, until time.Time, version string) (models.NetworkTrend, error) {
	key := fmt.Sprintf("%p/%s/%s/%s/%s/%s", db, user, id, version, since.Format(time.RFC3339Nano), until.Format(time.RFC3339Nano))
	return h.historyCache.get(ctx, key, func(task context.Context) (models.NetworkTrend, error) {
		return models.NetworkTrendHistory(task, db, id, user, since, until)
	})
}

func networkHistoryError(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(404, gin.H{"error": "check not found"})
		return
	}
	if errors.Is(err, models.ErrNetworkCursor) {
		c.JSON(400, gin.H{"error": "invalid network history cursor"})
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "failed to read network history"})
}

func (h *NetworkHandler) Trend(c *gin.Context)   { h.history(c, true) }
func (h *NetworkHandler) History(c *gin.Context) { h.history(c, false) }

func (h *NetworkHandler) history(c *gin.Context, trendOnly bool) {
	id, ok := networkID(c)
	if !ok {
		return
	}
	since, until, ok := networkHistoryRange(c)
	if !ok {
		return
	}
	limit := 300
	var err error
	if !trendOnly {
		if value := c.Query("limit"); value != "" {
			limit, err = strconv.Atoi(value)
		}
		if err != nil || limit < 1 || limit > 2000 {
			c.JSON(400, gin.H{"error": "history limit must be 1..2000"})
			return
		}
	}
	db, user := networkDB(c)
	cursor := c.Query("cursor")
	if !trendOnly {
		if err := models.ValidateNetworkHistoryCursor(cursor, id, user, since, until); err != nil {
			networkHistoryError(c, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	// Revalidate after loading: a concurrent source edit/delete or ownership
	// transfer must not revive a cached result for the former check/user.
	for attempt := 0; attempt < 2; attempt++ {
		version, err := models.NetworkHistoryVersion(ctx, db, id, user)
		if err != nil {
			networkHistoryError(c, err)
			return
		}
		trend, err := h.loadNetworkTrend(ctx, db, id, user, since, until, version)
		if err != nil {
			networkHistoryError(c, err)
			return
		}
		var samples []models.NetworkSample
		var next *string
		if !trendOnly {
			samples, next, err = models.NetworkHistoryPage(ctx, db, id, user, since, until, limit, cursor)
			if err != nil {
				networkHistoryError(c, err)
				return
			}
		}
		current, err := models.NetworkHistoryVersion(ctx, db, id, user)
		if err != nil {
			networkHistoryError(c, err)
			return
		}
		if version != current {
			continue
		}
		if trendOnly {
			c.JSON(200, trend)
		} else {
			c.JSON(200, gin.H{"samples": samples, "summary": trend.Summary, "truncated": next != nil, "next_cursor": next})
		}
		return
	}
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": "network history changed during query; retry"})
}
