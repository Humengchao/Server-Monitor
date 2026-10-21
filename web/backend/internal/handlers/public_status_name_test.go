package handlers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/models"
)

func TestPublicStatusPublishesDisplayNameWithoutConnectionFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const displayName = "香港 主数据库 <primary>"
	id := uuid.NewString()
	values := []driver.Value{
		id, displayName, "Hong Kong", "[]", "linux", int64(4), int64(1000), nil,
		float64(0), "CNY", "year", int64(0), float64(20), float64(0), float64(0), float64(0),
		int64(50), int64(100), int64(200), int64(1), int64(2), int64(3), int64(4),
		int64(3600), int64(10), time.Now(), nil,
	}
	db := sql.OpenDB(&metricsConnector{query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "server_metrics_15m") {
			return &metricsRows{columns: []string{"id", "observed", "expected"}, values: [][]driver.Value{{id, int64(100), int64(100)}}}, nil
		}
		if !strings.Contains(query, "SELECT s.id, s.name,") {
			t.Error("public query omitted the configured display name")
		}
		for _, privateField := range []string{"s.host", "s.port", "s.ssh_", "s.notes"} {
			if strings.Contains(query, privateField) {
				t.Errorf("public query selected %s", privateField)
			}
		}
		return &metricsRows{columns: make([]string, len(values)), values: [][]driver.Value{values}}, nil
	}})
	defer db.Close()
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("db", &models.DB{Raw: db}) })
	router.GET("/api/public/status", NewPublicStatusHandler().Get)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/public/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if strings.Contains(response.Body.String(), id) || strings.Contains(response.Body.String(), `"server_id"`) || strings.Contains(response.Body.String(), `"ServerID"`) {
		t.Fatal("internal join identity escaped into public JSON")
	}
	var body struct {
		Nodes   []map[string]any `json:"nodes"`
		Privacy struct {
			Anonymized   bool     `json:"anonymized"`
			HiddenFields []string `json:"hidden_fields"`
		} `json:"privacy"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Nodes) != 1 || body.Nodes[0]["name"] != displayName {
		t.Fatalf("display name lost: %#v", body.Nodes)
	}
	if body.Nodes[0]["alias"] != "NODE 01" {
		t.Fatal("compatibility identifier changed")
	}
	if body.Privacy.Anonymized {
		t.Fatal("named servers must not be described as anonymous")
	}
	for _, field := range body.Privacy.HiddenFields {
		if field == "server_name" || field == "name" {
			t.Fatal("display name incorrectly listed as hidden")
		}
		if _, exists := body.Nodes[0][field]; exists {
			t.Fatalf("private field exposed: %s", field)
		}
	}
}
