package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
	"server-monitor/internal/testdb"
)

func TestServerDeletionNeverDisconnectsAnotherUsersAgent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := testdb.Open(t)
	user, own, _ := testdb.Seed(t, raw)
	_, foreign, _ := testdb.Seed(t, raw)
	bulk := uuid.New()
	if _, err := raw.Exec(`INSERT INTO servers(id,user_id,name,host) VALUES($1,$2,'bulk','192.0.2.1')`, bulk, user); err != nil {
		t.Fatal(err)
	}
	disconnected := []uuid.UUID{}
	services.SetAgentConnector(nil, func(id uuid.UUID) { disconnected = append(disconnected, id) })
	t.Cleanup(func() { services.SetAgentConnector(nil, nil) })
	cache := services.NewSSHConnCache()
	defer cache.Close()
	invalidations := 0
	invalidate := func() { invalidations++ }
	single := NewServerHandler(cache, nil, invalidate)
	batch := NewBatchHandler(cache, invalidate)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("db", &models.DB{Raw: raw}); c.Set("user_id", user) })
	router.DELETE("/servers/:id", single.Delete)
	router.POST("/bulk", batch.BulkDelete)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	for _, id := range []uuid.UUID{foreign, uuid.New()} {
		if response := request("DELETE", "/servers/"+id.String(), ""); response.Code != 404 {
			t.Fatalf("unowned delete: %d %s", response.Code, response.Body)
		}
	}
	if len(disconnected) != 0 || invalidations != 0 {
		t.Fatal("unowned single deletion caused side effects")
	}
	response := request("POST", "/bulk", `{"server_ids":["`+foreign.String()+`","`+uuid.NewString()+`"]}`)
	var result struct {
		Deleted int `json:"deleted"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	if response.Code != 200 || result.Deleted != 0 || len(disconnected) != 0 || invalidations != 0 {
		t.Fatal("unowned batch caused side effects", response.Code, response.Body)
	}
	response = request("POST", "/bulk", `{"server_ids":["`+foreign.String()+`","`+bulk.String()+`","`+bulk.String()+`"]}`)
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	if response.Code != 200 || result.Deleted != 1 || len(disconnected) != 1 || disconnected[0] != bulk || invalidations != 1 {
		t.Fatal("mixed batch affected foreign connection", disconnected, response.Code, response.Body)
	}
	if response = request("DELETE", "/servers/"+own.String(), ""); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if len(disconnected) != 2 || disconnected[1] != own || invalidations != 2 {
		t.Fatal("owned deletion did not disconnect expected connection", disconnected)
	}
	var remains bool
	if err := raw.QueryRow(`SELECT EXISTS(SELECT 1 FROM servers WHERE id=$1)`, foreign).Scan(&remains); err != nil || !remains {
		t.Fatal("foreign server was deleted", err)
	}
}
