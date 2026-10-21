package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/models"
)

func uptimeDetailTestRouter(db *sql.DB, user uuid.UUID) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("user_id", user)
		c.Set("db", &models.DB{Raw: db})
	})
	router.GET("/servers/:id/uptime", NewUptimeHandler().Detail)
	return router
}

func uptimeDetailFixtureRows(stage int) *metricsRows {
	switch stage {
	case 1:
		return &metricsRows{columns: []string{"created_at"}, values: [][]driver.Value{{time.Now().Add(-60 * 24 * time.Hour)}}}
	case 2:
		return &metricsRows{columns: []string{"day", "count"}}
	case 3:
		return &metricsRows{columns: []string{"prev_at", "recorded_at"}}
	default:
		return &metricsRows{columns: []string{"last_at"}, values: [][]driver.Value{{nil}}}
	}
}

func TestUptimeDetailCancellationStopsEveryQueryStage(t *testing.T) {
	for blocked := 1; blocked <= 4; blocked++ {
		t.Run([]string{"ownership", "daily", "gaps", "latest"}[blocked-1], func(t *testing.T) {
			user, server := uuid.New(), uuid.New()
			entered := make(chan struct{})
			var calls atomic.Int32
			var opened []*metricsRows
			db := sql.OpenDB(&metricsConnector{query: func(ctx context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
				stage := int(calls.Add(1))
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > uptimeDetailTimeout {
					t.Error("detail query has no bounded request context")
				}
				if len(args) == 0 || args[0].Value != server.String() || stage == 1 && (len(args) != 2 || args[1].Value != user.String()) {
					t.Error("detail query lost server/owner scope")
				}
				if stage == blocked {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				rows := uptimeDetailFixtureRows(stage)
				opened = append(opened, rows)
				return rows, nil
			}})
			defer db.Close()
			db.SetMaxOpenConns(1)
			router := uptimeDetailTestRouter(db, user)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/servers/"+server.String()+"/uptime", nil).WithContext(ctx))
				close(done)
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("request did not reach blocked query")
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("handler ignored request cancellation")
			}
			if response.Code != http.StatusServiceUnavailable || calls.Load() != int32(blocked) || db.Stats().InUse != 0 {
				t.Fatalf("cancelled request continued work: status=%d calls=%d in_use=%d", response.Code, calls.Load(), db.Stats().InUse)
			}
			for _, rows := range opened {
				if !rows.closed {
					t.Fatal("completed detail query leaked its rows")
				}
			}
		})
	}
}

func TestUptimeDetailDistinguishesMissingHostFromDatabaseFailure(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want int
	}{
		{"missing", nil, http.StatusNotFound},
		{"database_failure", errors.New("private database diagnostic"), http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := sql.OpenDB(&metricsConnector{query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
				if test.err != nil {
					return nil, test.err
				}
				return &metricsRows{columns: []string{"created_at"}}, nil
			}})
			defer db.Close()
			response := httptest.NewRecorder()
			uptimeDetailTestRouter(db, uuid.New()).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/servers/"+uuid.NewString()+"/uptime", nil))
			if response.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.want, response.Body)
			}
			if test.err != nil && response.Body.String() != `{"error":"failed to load availability"}` {
				t.Fatalf("database failure leaked or misreported: %s", response.Body)
			}
		})
	}
}
