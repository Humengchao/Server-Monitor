package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/models"
)

func TestFleetUptimeCoalescesRefreshAndSeparatesUsers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	db := sql.OpenDB(&metricsConnector{query: func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
		call := calls.Add(1)
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 11*time.Second {
			t.Error("census query has no bounded context")
		}
		if call == 1 {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &metricsRows{columns: []string{"unused"}}, nil
	}})
	defer db.Close()
	user, other := uuid.New(), uuid.New()
	h := NewUptimeHandler()
	r := gin.New()
	started := make(chan struct{}, 10)
	r.Use(func(c *gin.Context) {
		c.Set("db", &models.DB{Raw: db})
		id := user
		if c.GetHeader("X-Test-User") == "other" {
			id = other
		}
		c.Set("user_id", id)
		started <- struct{}{}
	})
	r.GET("/uptime", h.Fleet)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest("GET", "/uptime", nil).WithContext(ctx))
		first <- response.Code
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not start")
	}
	var visitors sync.WaitGroup
	for i := 0; i < 4; i++ {
		visitors.Add(1)
		go func() {
			defer visitors.Done()
			response := httptest.NewRecorder()
			r.ServeHTTP(response, httptest.NewRequest("GET", "/uptime", nil))
			if response.Code != 200 {
				t.Errorf("visitor: %d", response.Code)
			}
		}()
	}
	for i := 0; i < 5; i++ {
		<-started
	}
	cancel()
	select {
	case code := <-first:
		if code != 503 {
			t.Errorf("canceled waiter: %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled waiter blocked")
	}
	close(release)
	visitors.Wait()
	if calls.Load() != 2 {
		t.Fatalf("shared census made %d queries instead of two tiers", calls.Load())
	}
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest("GET", "/uptime", nil))
	if response.Code != 200 || calls.Load() != 2 {
		t.Fatal("fresh census was not cached")
	}
	request := httptest.NewRequest("GET", "/uptime", nil)
	request.Header.Set("X-Test-User", "other")
	response = httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != 200 || calls.Load() != 4 {
		t.Fatal("a different user's census shared the first user's cache")
	}
}
