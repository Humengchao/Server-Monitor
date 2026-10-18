package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func alertSummaryRouter(t *testing.T, query metricsQueryFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	userID := uuid.New()
	// Reuse the lightweight database/sql connector from the metrics regressions.
	db := sql.OpenDB(&metricsConnector{query: func(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
		if len(args) != 1 || args[0].Value != userID.String() {
			t.Errorf("summary query is not scoped to the authenticated user: %v", args)
		}
		return query(ctx, statement, args)
	}})
	t.Cleanup(func() { db.Close() })
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", userID)
		c.Set("db", &models.DB{Raw: db})
	})
	router.GET("/alerts/summary", NewAlertHandler(nil).Summary)
	return router
}

func TestAlertServerSummaryIncludesEveryFiringHost(t *testing.T) {
	rows := &metricsRows{columns: []string{"server_id", "active", "rule_names"}}
	for i := 0; i < 205; i++ {
		rows.values = append(rows.values, []driver.Value{uuid.NewString(), int64(1), `{"CPU high"}`})
	}
	// Events whose rules no longer exist still count, even without a tooltip.
	rows.values[0][1], rows.values[0][2] = int64(251), "{}"
	queries := 0
	router := alertSummaryRouter(t, func(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
		queries++
		query := strings.Join(strings.Fields(statement), " ")
		if strings.Contains(strings.ToUpper(query), "LIMIT") ||
			!strings.Contains(query, "WHERE e.user_id = $1 AND e.resolved_at IS NULL") ||
			!strings.Contains(query, "LEFT JOIN alert_rules r ON r.id = e.rule_id AND r.user_id = e.user_id") {
			t.Fatalf("summary must include all active events, with owner-scoped optional rule names: %s", query)
		}
		return rows, nil
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/alerts/summary?by_server=1", nil))
	var summary models.ActiveAlertSummary
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &summary) != nil {
		t.Fatalf("unexpected summary response: %d %s", response.Code, response.Body)
	}
	if summary.Active != 455 || len(summary.Servers) != 205 || summary.Servers[0].Active != 251 {
		t.Fatalf("summary omitted active events: active=%d hosts=%d", summary.Active, len(summary.Servers))
	}
	if len(summary.Servers[0].RuleNames) != 0 || summary.Servers[1].RuleNames[0] != "CPU high" {
		t.Fatal("missing rule names changed counts or available tooltip names")
	}
	if queries != 1 || !rows.closed {
		t.Fatalf("expected one query and closed rows, got queries=%d closed=%v", queries, rows.closed)
	}
}

func TestAlertSummaryDistinguishesEmptyAndFailedQueries(t *testing.T) {
	failure := errors.New("private database error")
	for _, tc := range []struct {
		name       string
		queryErr   error
		streamErr  error
		values     [][]driver.Value
		wantStatus int
	}{
		{name: "empty", wantStatus: 200},
		{name: "query failure", queryErr: failure, wantStatus: 500},
		{name: "invalid row", values: [][]driver.Value{{"invalid uuid", int64(1), "{}"}}, wantStatus: 500},
		{name: "partial stream failure", values: [][]driver.Value{{uuid.NewString(), int64(1), "{}"}}, streamErr: failure, wantStatus: 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := &metricsRows{columns: []string{"server_id", "active", "rule_names"}, values: tc.values}
			if tc.streamErr != nil {
				rows.end = func() error { return tc.streamErr }
			}
			router := alertSummaryRouter(t, func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
				return rows, tc.queryErr
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/alerts/summary?by_server=1", nil))
			if response.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if tc.wantStatus == 200 && response.Body.String() != `{"active":0,"servers":[]}` {
				t.Fatalf("empty summary must return an empty list: %s", response.Body)
			}
			if tc.wantStatus == 500 && response.Body.String() != `{"error":"failed to load alert summary"}` {
				t.Fatalf("failed summary must not leak errors or partial counts: %s", response.Body)
			}
			if tc.queryErr == nil && !rows.closed {
				t.Fatal("summary rows were not released")
			}
		})
	}
}

func TestAlertHeaderSummaryKeepsCountOnlyResponse(t *testing.T) {
	router := alertSummaryRouter(t, func(_ context.Context, statement string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(statement, "GROUP BY") || !strings.Contains(statement, "user_id=$1") {
			t.Fatalf("header badge should keep its owner-scoped count query: %s", statement)
		}
		return &metricsRows{columns: []string{"active"}, values: [][]driver.Value{{int64(305)}}}, nil
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/alerts/summary", nil))
	if response.Code != 200 || response.Body.String() != `{"active":305}` {
		t.Fatalf("header summary response changed: %d %s", response.Code, response.Body)
	}
}

func TestAlertServerSummaryCancelsDatabaseQuery(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	router := alertSummaryRouter(t, func(ctx context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		defer close(finished)
		request := httptest.NewRequest(http.MethodGet, "/alerts/summary?by_server=1", nil).WithContext(ctx)
		router.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("summary query never started")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("HTTP cancellation did not stop the database query")
	}
}
