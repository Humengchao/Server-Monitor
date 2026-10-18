package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func TestServerUpdateReportsExpiryConflictWithoutChangingMetadata(t *testing.T) {
	db := testdb.Open(t)
	user, id, _ := testdb.Seed(t, db)
	current := time.Date(2030, 5, 1, 0, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`UPDATE servers SET expires_at=$1 WHERE id=$2`, current, id); err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("db", &models.DB{Raw: db, EncryptionKey: "0123456789abcdef0123456789abcdef"})
		c.Set("user_id", user)
	})
	router.PUT("/servers/:id", NewServerHandler(nil, nil).Update)
	for _, expected := range []any{nil, current.AddDate(0, -1, 0).Format(time.RFC3339)} {
		body, _ := json.Marshal(map[string]any{"name": "stale name", "host": "192.0.2.2", "ssh_username": "root", "ssh_password": "test-only",
			"expires_at": "2030-04-01T00:00:00Z", "expected_expires_at": expected})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPut, "/servers/"+id.String(), bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		var problem map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if response.Code != 409 || problem["error_code"] != "expiry_changed" {
			t.Fatalf("response=%d %s", response.Code, response.Body)
		}
	}
	var saved time.Time
	var name string
	if err := db.QueryRow(`SELECT expires_at,name FROM servers WHERE id=$1`, id).Scan(&saved, &name); err != nil {
		t.Fatal(err)
	}
	if name != "test" || !saved.Equal(current) {
		t.Fatal("conflicting edit was partly applied")
	}
	if _, err := db.Exec(`UPDATE servers SET expires_at=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"name": "manual edit", "host": "192.0.2.1", "ssh_username": "root", "ssh_password": "test-only",
		"expires_at": current.Format(time.RFC3339), "expected_expires_at": nil})
	request := httptest.NewRequest(http.MethodPut, "/servers/"+id.String(), bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("matching NULL expectation failed: %d %s", response.Code, response.Body)
	}
}
