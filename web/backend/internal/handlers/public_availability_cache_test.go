package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/models"
)

func publicMetricFixture(id uuid.UUID, location string, cpu int64) []driver.Value {
	return []driver.Value{id.String(), "same display name", location, "[]", "linux", int64(4), int64(1000), nil,
		float64(0), "CNY", "year", int64(0), float64(cpu), float64(0), float64(0), float64(0),
		int64(50), int64(100), int64(200), int64(1), int64(2), int64(3), int64(4), int64(3600), int64(10), time.Now(), nil}
}

func publicCacheRouter(h *PublicStatusHandler, db *sql.DB) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("db", &models.DB{Raw: db}) })
	router.GET("/status", h.Get)
	return router
}

func publicCacheRequest(router *gin.Engine) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest("GET", "/status", nil))
	return response
}

func TestPublicRealtimeRefreshReusesAvailabilityByStableIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	first, second := uuid.New(), uuid.New()
	var metricQueries, censusQueries atomic.Int32
	db := sql.OpenDB(&metricsConnector{query: func(ctx context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("database work lacks a deadline")
		}
		if strings.Contains(query, "server_metrics_15m") {
			censusQueries.Add(1)
			return &metricsRows{columns: []string{"id", "observed", "expected"}, values: [][]driver.Value{{second.String(), int64(50), int64(100)}, {first.String(), int64(100), int64(100)}}}, nil
		}
		call := metricQueries.Add(1)
		rows := [][]driver.Value{publicMetricFixture(first, "A", int64(call)*10), publicMetricFixture(second, "B", int64(call)*10)}
		if call > 1 {
			rows[0], rows[1] = rows[1], rows[0]
		}
		return &metricsRows{columns: make([]string, len(rows[0])), values: rows}, nil
	}})
	defer db.Close()
	h := NewPublicStatusHandler()
	router := publicCacheRouter(h, db)
	for i := 1; i <= 2; i++ {
		response := publicCacheRequest(router)
		var body struct {
			Nodes []publicNode `json:"nodes"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Nodes) != 2 {
			t.Fatalf("snapshot: %d %s", response.Code, response.Body)
		}
		for _, node := range body.Nodes {
			want := float64(100)
			if node.Location == "B" {
				want = 50
			}
			if node.CPUPercent != i*10 || node.Availability30d == nil || *node.Availability30d != want {
				t.Fatalf("snapshots joined by name/order instead of ID: %+v", node)
			}
		}
		if strings.Contains(response.Body.String(), first.String()) || strings.Contains(response.Body.String(), second.String()) {
			t.Fatal("internal UUID escaped")
		}
		// Expire only the real-time response without a 2-second test sleep.
		h.cache.Forget("public")
	}
	if metricQueries.Load() != 2 || censusQueries.Load() != 1 {
		t.Fatalf("queries: current=%d census=%d", metricQueries.Load(), censusQueries.Load())
	}
}

func TestPublicAvailabilityKeyTracksWindowVisibilityAndGeneration(t *testing.T) {
	first, second := models.PublicServerMetric{ServerID: uuid.New()}, models.PublicServerMetric{ServerID: uuid.New()}
	end := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	key, _ := publicAvailabilityKey(0, end, []models.PublicServerMetric{first, second})
	reordered, _ := publicAvailabilityKey(0, end, []models.PublicServerMetric{second, first})
	if key != reordered {
		t.Fatal("display ordering invalidates the census")
	}
	for _, change := range []struct {
		generation uint64
		end        time.Time
		metrics    []models.PublicServerMetric
	}{
		{1, end, []models.PublicServerMetric{first, second}},
		{0, end.Add(15 * time.Minute), []models.PublicServerMetric{first, second}},
		{0, end, []models.PublicServerMetric{first}},
	} {
		if changed, _ := publicAvailabilityKey(change.generation, change.end, change.metrics); changed == key {
			t.Fatal("window/visibility generation reused stale census")
		}
	}
}

func TestPublicInvalidationIsolatesBothInflightCacheStages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, phase := range []string{"before_census", "during_census"} {
		t.Run(phase, func(t *testing.T) {
			oldID, newID := uuid.New(), uuid.New()
			entered, release := make(chan struct{}), make(chan struct{})
			var metricQueries, newCensusQueries atomic.Int32
			db := sql.OpenDB(&metricsConnector{query: func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
				block := func() error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				if strings.Contains(query, "server_metrics_15m") {
					id := newID
					if strings.Contains(args[2].Value.(string), oldID.String()) {
						id = oldID
						if phase == "during_census" {
							if err := block(); err != nil {
								return nil, err
							}
						}
					} else {
						newCensusQueries.Add(1)
					}
					return &metricsRows{columns: []string{"id", "observed", "expected"}, values: [][]driver.Value{{id.String(), int64(100), int64(100)}}}, nil
				}
				id, location := newID, "new public server"
				if metricQueries.Add(1) == 1 {
					id, location = oldID, "now private server"
					if phase == "before_census" {
						if err := block(); err != nil {
							return nil, err
						}
					}
				}
				row := publicMetricFixture(id, location, 10)
				return &metricsRows{columns: make([]string, len(row)), values: [][]driver.Value{row}}, nil
			}})
			defer db.Close()
			h := NewPublicStatusHandler()
			router := publicCacheRouter(h, db)
			oldDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { oldDone <- publicCacheRequest(router) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("old refresh did not reach blocking stage")
			}
			h.Invalidate()
			current := publicCacheRequest(router)
			if current.Code != 200 || strings.Contains(current.Body.String(), "now private server") {
				t.Fatalf("current generation: %d %s", current.Code, current.Body)
			}
			close(release)
			select {
			case old := <-oldDone:
				if old.Code != 200 || strings.Contains(old.Body.String(), "now private server") {
					t.Fatalf("old waiter published stale visibility: %d %s", old.Code, old.Body)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("old waiter did not join current generation")
			}
			h.cache.Forget("public")
			last := publicCacheRequest(router)
			if last.Code != 200 || strings.Contains(last.Body.String(), "now private server") || newCensusQueries.Load() != 1 {
				t.Fatalf("old refresh contaminated current census: calls=%d %d %s", newCensusQueries.Load(), last.Code, last.Body)
			}
		})
	}
}
