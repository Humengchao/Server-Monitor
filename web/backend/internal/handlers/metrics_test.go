package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Exercise the HTTP handlers and database/sql cancellation without PostgreSQL.
type metricsQueryFunc func(context.Context, string, []driver.NamedValue) (driver.Rows, error)

type metricsConnector struct{ query metricsQueryFunc }

func (c *metricsConnector) Connect(context.Context) (driver.Conn, error) {
	return &metricsConnection{query: c.query}, nil
}
func (*metricsConnector) Driver() driver.Driver { return metricsDriver{} }

type metricsDriver struct{}

func (metricsDriver) Open(string) (driver.Conn, error) { return nil, errors.New("use connector") }

type metricsConnection struct{ query metricsQueryFunc }

func (*metricsConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*metricsConnection) Close() error { return nil }
func (*metricsConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (c *metricsConnection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.query(ctx, query, args)
}

type metricsRows struct {
	columns []string
	values  [][]driver.Value
	index   int
	end     func() error
	closed  bool
}

func (r *metricsRows) Columns() []string { return r.columns }
func (r *metricsRows) Close() error      { r.closed = true; return nil }
func (r *metricsRows) Next(dest []driver.Value) error {
	if r.index == len(r.values) {
		if r.end != nil {
			return r.end()
		}
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

var metricsColumns = []string{
	"cpu_percent", "load_1", "load_5", "load_15", "memory_used", "memory_total", "disk_used_bytes",
	"network_rx_bytes", "network_tx_bytes", "network_rx_total_bytes", "network_tx_total_bytes",
	"disk_rx_bytes", "disk_tx_bytes", "uptime_seconds", "latency_ms", "recorded_at",
}

func metricFixtureRow() []driver.Value {
	return []driver.Value{12.5, 0.1, 0.2, 0.3, int64(2), int64(8), int64(20),
		int64(1), int64(2), int64(3), int64(4), int64(5), int64(6), int64(3600), int64(7),
		time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func metricsTestHandler(t *testing.T, query metricsQueryFunc) (*gin.Engine, *sql.DB, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	serverID, userID := uuid.New(), uuid.New()
	db := sql.OpenDB(&metricsConnector{query: func(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
		if len(args) == 0 || args[0].Value != serverID.String() {
			t.Errorf("wrong server in query arguments: %v", args)
		}
		if strings.Contains(statement, "SELECT EXISTS") && (len(args) != 2 || args[1].Value != userID.String()) {
			t.Errorf("ownership query is not scoped to the current user: %v", args)
		}
		return query(ctx, statement, args)
	}})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("db", &models.DB{Raw: db})
	})
	handler := NewMetricsHandler()
	router.GET("/servers/:id/metrics/latest", handler.GetLatest)
	router.GET("/servers/:id/metrics", handler.GetHistory)
	return router, db, "/servers/" + serverID.String() + "/metrics"
}

func TestMetricsResponsesDistinguishEmptyAndFailedQueries(t *testing.T) {
	failure := errors.New("private database failure detail")
	for _, test := range []struct {
		name       string
		suffix     string
		owned      bool
		ownerErr   error
		queryErr   error
		rows       [][]driver.Value
		streamErr  error
		wantStatus int
		wantBody   string
	}{
		{name: "latest never sampled", suffix: "/latest", owned: true, wantStatus: 200, wantBody: "null"},
		{name: "latest database failure", suffix: "/latest", owned: true, queryErr: failure, wantStatus: 500},
		{name: "latest row failure", suffix: "/latest", owned: true, streamErr: failure, wantStatus: 500},
		{name: "latest successful", suffix: "/latest", owned: true, rows: [][]driver.Value{metricFixtureRow()}, wantStatus: 200},
		{name: "history empty", owned: true, wantStatus: 200, wantBody: "[]"},
		{name: "history database failure", owned: true, queryErr: failure, wantStatus: 500},
		{name: "history partial stream failure", owned: true, rows: [][]driver.Value{metricFixtureRow()}, streamErr: failure, wantStatus: 500},
		{name: "history successful", owned: true, rows: [][]driver.Value{metricFixtureRow()}, wantStatus: 200},
		{name: "latest ownership failure", suffix: "/latest", ownerErr: failure, wantStatus: 500},
		{name: "history ownership failure", ownerErr: failure, wantStatus: 500},
		{name: "latest unowned", suffix: "/latest", wantStatus: 404},
		{name: "history unowned", wantStatus: 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := &metricsRows{columns: metricsColumns, values: test.rows}
			if test.streamErr != nil {
				rows.end = func() error { return test.streamErr }
			}
			metricQueries := 0
			router, db, path := metricsTestHandler(t, func(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
				if strings.Contains(statement, "SELECT EXISTS") {
					if test.ownerErr != nil {
						return nil, test.ownerErr
					}
					return &metricsRows{columns: []string{"owned"}, values: [][]driver.Value{{test.owned}}}, nil
				}
				metricQueries++
				if test.queryErr != nil {
					return nil, test.queryErr
				}
				return rows, nil
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+test.suffix, nil))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, test.wantStatus, response.Body)
			}
			if test.wantBody != "" && response.Body.String() != test.wantBody {
				t.Fatalf("body = %q, want %q", response.Body.String(), test.wantBody)
			}
			if test.wantStatus == 200 && len(test.rows) > 0 {
				var point models.MetricPoint
				if test.suffix == "/latest" {
					if err := json.Unmarshal(response.Body.Bytes(), &point); err != nil {
						t.Fatal(err)
					}
				} else {
					var points []models.MetricPoint
					if err := json.Unmarshal(response.Body.Bytes(), &points); err != nil || len(points) != 1 {
						t.Fatalf("history body = %s, error = %v", response.Body, err)
					}
					point = points[0]
				}
				if point.CPUPercent != 12.5 || point.UptimeSeconds != 3600 || point.RecordedAt.Format(time.RFC3339) != "2030-01-02T03:04:05Z" {
					t.Fatalf("metric fields changed: %+v", point)
				}
			}
			if test.wantStatus >= 400 {
				var body map[string]string
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body["error"] == "" {
					t.Fatalf("expected an error object, got %s", response.Body)
				}
				if strings.Contains(response.Body.String(), failure.Error()) {
					t.Fatal("response leaked a database error")
				}
			}
			if test.owned {
				if metricQueries != 1 {
					t.Fatalf("metric queries = %d, want 1", metricQueries)
				}
				if test.queryErr == nil && !rows.closed {
					t.Fatal("metric rows were not closed")
				}
			} else if metricQueries != 0 {
				t.Fatal("metrics queried before successful ownership verification")
			}
			if db.Stats().InUse != 0 {
				t.Fatal("handler retained its database connection")
			}
		})
	}
}

func TestMetricsRequestsCancelDatabaseWork(t *testing.T) {
	for _, test := range []struct{ name, suffix, stage string }{
		{"latest ownership", "/latest", "ownership"},
		{"history ownership", "", "ownership"},
		{"latest query", "/latest", "query"},
		{"history query", "", "query"},
		{"history streaming rows", "", "rows"},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{})
			rescue := make(chan struct{})
			finished := make(chan struct{})
			defer func() {
				close(rescue)
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("handler did not exit after releasing the test query")
				}
			}()
			var queryErr error
			waitForCancellation := func(ctx context.Context) error {
				close(started)
				select {
				case <-ctx.Done():
					queryErr = ctx.Err()
					return queryErr
				case <-rescue:
					return errors.New("query did not observe request cancellation")
				}
			}
			router, db, path := metricsTestHandler(t, func(ctx context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
				if strings.Contains(statement, "SELECT EXISTS") {
					if test.stage == "ownership" {
						return nil, waitForCancellation(ctx)
					}
					return &metricsRows{columns: []string{"owned"}, values: [][]driver.Value{{true}}}, nil
				}
				if test.stage == "query" {
					return nil, waitForCancellation(ctx)
				}
				return &metricsRows{columns: metricsColumns, values: [][]driver.Value{metricFixtureRow()}, end: func() error {
					return waitForCancellation(ctx)
				}}, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			response := httptest.NewRecorder()
			go func() {
				defer close(finished)
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path+test.suffix, nil).WithContext(ctx))
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("request never reached the blocked database operation")
			}
			cancel()
			select {
			case <-finished:
			case <-time.After(2 * time.Second):
				t.Fatal("database operation outlived the canceled HTTP request")
			}
			if !errors.Is(queryErr, context.Canceled) {
				t.Fatalf("driver error = %v, want context.Canceled", queryErr)
			}
			if response.Code == http.StatusOK {
				t.Fatalf("canceled query reported success: %s", response.Body)
			}
			if db.Stats().InUse != 0 {
				t.Fatal("canceled request retained its database connection")
			}
		})
	}
}
