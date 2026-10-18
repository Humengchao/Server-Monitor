package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"net/http/httptest"
	"server-monitor/internal/models"
	"strings"
	"testing"
	"time"
)

func TestNetworkListScopesOwnerAndPreservesUnknown(t *testing.T) {
	user, id, target := uuid.New(), uuid.New(), uuid.New()
	db := sql.OpenDB(&metricsConnector{query: func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "c.user_id=$1") || len(args) != 1 || args[0].Value != user.String() {
			t.Fatalf("unscoped: %s %v", query, args)
		}
		return &metricsRows{columns: []string{"id", "user", "name", "source", "target", "packets", "enabled", "loss", "latency", "created", "sample"}, values: [][]driver.Value{{id.String(), user.String(), "check", nil, target.String(), int64(5), true, float64(10), nil, time.Now(), []byte(`{"status":"error","sent":null,"received":null,"loss_percent":null}`)}}}, nil
	}})
	defer db.Close()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("db", &models.DB{Raw: db}); c.Set("user_id", user) })
	r.GET("/checks", NewNetworkHandler(nil).List)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest("GET", "/checks", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"loss_percent":null`) {
		t.Fatalf("%d %s", response.Code, response.Body)
	}
}
func TestLogsQueryValidationBeforeHostLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewLogsHandler(nil)
	r.GET("/servers/:id/logs", h.Journal)
	r.GET("/servers/:id/docker/containers/:containerId/logs/page", h.Docker)
	for _, path := range []string{"/servers/x/logs?limit=501", "/servers/x/logs?since=bad", "/servers/x/logs?unit=a%3Bid", "/servers/x/logs?priority=8", "/servers/x/docker/containers/not-hex/logs/page"} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 400 {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body)
		}
	}
}
