package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"server-monitor/internal/models"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicStatusCoalescesVisitorsAndCanceledWaiter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var queries atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	db := sql.OpenDB(&metricsConnector{query: func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
		queries.Add(1)
		close(entered)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("refresh has no database deadline")
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &metricsRows{columns: []string{"unused"}}, nil
	}})
	defer db.Close()
	h := NewPublicStatusHandler()
	r := gin.New()
	started := make(chan struct{}, 12)
	r.Use(func(c *gin.Context) { c.Set("db", &models.DB{Raw: db}); started <- struct{}{} })
	r.GET("/status", h.Get)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil).WithContext(ctx))
		first <- w.Code
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh never started")
	}
	var wg sync.WaitGroup
	for i := 0; i < 11; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
			if w.Code != 200 {
				t.Errorf("visitor: %d", w.Code)
			}
		}()
	}
	for i := 0; i < 12; i++ {
		<-started
	}
	cancel()
	select {
	case code := <-first:
		if code != 503 {
			t.Errorf("canceled visitor: %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled waiter did not return")
	}
	close(release)
	wg.Wait()
	if queries.Load() != 1 {
		t.Fatalf("12 visitors caused %d queries", queries.Load())
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != 200 || queries.Load() != 1 {
		t.Fatal("cached payload was not reused")
	}
}

func TestPublicStatusFailureIsRetried(t *testing.T) {
	gin.SetMode(gin.TestMode)
	count := 0
	db := sql.OpenDB(&metricsConnector{query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
		count++
		if count == 1 {
			return nil, errors.New("database unavailable")
		}
		return &metricsRows{columns: []string{"unused"}}, nil
	}})
	defer db.Close()
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("db", &models.DB{Raw: db}) })
	r.GET("/status", NewPublicStatusHandler().Get)
	for _, expected := range []int{503, 200} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
		if w.Code != expected {
			t.Fatalf("status=%d want=%d", w.Code, expected)
		}
	}
}
