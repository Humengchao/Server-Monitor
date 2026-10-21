package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"server-monitor/internal/models"
	"server-monitor/internal/testdb"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func TestNetworkHistoryHandlersInvalidateSamplesPairEditsAndDeletedChecks(t *testing.T) {
	db := testdb.Open(t)
	user, server, _ := testdb.Seed(t, db)
	other, _, _ := testdb.Seed(t, db)
	check := models.NetworkCheck{ID: uuid.New(), UserID: user, Name: "cached", TargetServerID: server, Packets: 5, Enabled: true, LossThreshold: 10}
	ctx := context.Background()
	if err := models.SaveNetworkCheck(ctx, db, &check, true); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	five := 5
	avg := 10.0
	insert := func(at time.Time) {
		t.Helper()
		if err := models.InsertNetworkSample(ctx, db, check, models.NetworkSample{RecordedAt: at, Status: "ok", Sent: &five, Received: &five, AvgMS: &avg}); err != nil {
			t.Fatal(err)
		}
	}
	insert(since.Add(time.Minute))
	gin.SetMode(gin.TestMode)
	h := NewNetworkHandler(nil)
	r := gin.New()
	requestUser := user
	r.Use(func(c *gin.Context) { c.Set("db", &models.DB{Raw: db}); c.Set("user_id", requestUser) })
	r.GET("/checks/:check_id/trend", h.Trend)
	r.GET("/checks/:check_id/history", h.History)
	params := url.Values{"since": {since.Format(time.RFC3339)}, "until": {until.Format(time.RFC3339)}, "limit": {"1"}}
	request := func(kind string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/checks/"+check.ID.String()+"/"+kind+"?"+params.Encode(), nil))
		return w
	}
	assertCount := func(w *httptest.ResponseRecorder, want int) {
		t.Helper()
		var payload struct{ Summary models.NetworkSummary }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil || payload.Summary.SampleCount != want {
			t.Fatalf("status %d want count %d: %s", w.Code, want, w.Body)
		}
	}
	assertCount(request("trend"), 1)
	assertCount(request("history"), 1)
	if len(h.historyCache.entries) != 1 {
		t.Fatalf("trend and summary did not share result: %d", len(h.historyCache.entries))
	}
	insert(since.Add(2 * time.Minute))
	assertCount(request("trend"), 2)
	w := request("history")
	assertCount(w, 2)
	var page struct {
		Next      *string                `json:"next_cursor"`
		Truncated bool                   `json:"truncated"`
		Samples   []models.NetworkSample `json:"samples"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Next == nil || !page.Truncated || len(page.Samples) != 1 {
		t.Fatalf("missing cursor: %s %v", w.Body, err)
	}
	params.Set("cursor", *page.Next)
	w = request("history")
	page.Next = nil
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Next != nil || page.Truncated || len(page.Samples) != 1 || !page.Samples[0].RecordedAt.Equal(since.Add(time.Minute)) {
		t.Fatalf("oldest raw page inaccessible: %s %v", w.Body, err)
	}
	params.Del("cursor")
	requestUser = other
	if w := request("trend"); w.Code != 404 {
		t.Fatalf("cached result leaked to another user: %s", w.Body)
	}
	requestUser = user
	// Editing the source deletes incomparable history and must bypass its cache.
	check.SourceServerID = &server
	if err := models.SaveNetworkCheck(ctx, db, &check, false); err != nil {
		t.Fatal(err)
	}
	assertCount(request("trend"), 0)
	insert(since.Add(3 * time.Minute))
	assertCount(request("trend"), 1)
	if err := models.DeleteNetworkCheck(ctx, db, check.ID, user); err != nil {
		t.Fatal(err)
	}
	if w := request("trend"); w.Code != 404 {
		t.Fatalf("deleted history reappeared: %s", w.Body)
	}
}

func TestNetworkHistoryInvalidRequestsBeforeDatabaseAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewNetworkHandler(nil)
	r.GET("/checks/:check_id/trend", h.Trend)
	r.GET("/checks/:check_id/history", h.History)
	id := uuid.New().String()
	for _, path := range []string{
		"/checks/bad/trend", "/checks/" + id + "/trend?since=bad",
		"/checks/" + id + "/trend?since=2026-01-01T00:00:00Z&until=2026-01-09T00:00:00Z",
		"/checks/" + id + "/history?limit=0", "/checks/" + id + "/history?limit=2001",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 400 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
}

func TestNetworkTrendCacheCoalescesAndCancelsOnlyFinalWaiter(t *testing.T) {
	cache := newNetworkTrendCache()
	first, cancelFirst := context.WithCancel(context.Background())
	second, cancelSecond := context.WithCancel(context.Background())
	defer cancelFirst()
	defer cancelSecond()
	started := make(chan struct{})
	stopped := make(chan struct{})
	var loads atomic.Int32
	load := func(ctx context.Context) (models.NetworkTrend, error) {
		loads.Add(1)
		close(started)
		<-ctx.Done()
		close(stopped)
		return models.NetworkTrend{}, ctx.Err()
	}
	firstResult := make(chan error, 1)
	secondResult := make(chan error, 1)
	go func() { _, err := cache.get(first, "same", load); firstResult <- err }()
	<-started
	go func() { _, err := cache.get(second, "same", load); secondResult <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		cache.mu.Lock()
		waiters := cache.entries["same"].waiters
		cache.mu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second caller did not join")
		}
		time.Sleep(time.Millisecond)
	}
	cancelFirst()
	if err := <-firstResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("first cancellation: %v", err)
	}
	select {
	case <-stopped:
		t.Fatal("one disconnect canceled another caller")
	default:
	}
	cancelSecond()
	if err := <-secondResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("second cancellation: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("last disconnect left SQL running")
	}
	if loads.Load() != 1 {
		t.Fatalf("duplicate aggregate loads: %d", loads.Load())
	}
}

func TestNetworkTrendCacheBoundsAndExpiresResults(t *testing.T) {
	cache := newNetworkTrendCache()
	var loads int
	load := func(context.Context) (models.NetworkTrend, error) {
		loads++
		return models.NetworkTrend{Summary: models.NetworkSummary{SampleCount: loads}}, nil
	}
	ctx := context.Background()
	a, err := cache.get(ctx, "same", load)
	if err != nil {
		t.Fatal(err)
	}
	b, err := cache.get(ctx, "same", load)
	if err != nil || a.Summary.SampleCount != b.Summary.SampleCount || loads != 1 {
		t.Fatal("completed result was not cached")
	}
	cache.mu.Lock()
	cache.entries["same"].expires = time.Now().Add(-time.Second)
	cache.mu.Unlock()
	if _, err := cache.get(ctx, "same", load); err != nil || loads != 2 {
		t.Fatalf("expired result reused: %v", err)
	}
	for i := 0; i < networkTrendCacheLimit+10; i++ {
		if _, err := cache.get(ctx, fmt.Sprint(i), load); err != nil {
			t.Fatal(err)
		}
	}
	cache.mu.Lock()
	size := len(cache.entries)
	cache.mu.Unlock()
	if size > networkTrendCacheLimit {
		t.Fatalf("unbounded result cache: %d", size)
	}
}
