package handlers

import (
	"bytes"
	"context"
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

func TestAITaskHTTPScopesOwnerAndRejectsDecisionOverrides(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := &models.DB{Raw: testdb.Open(t), EncryptionKey: "0123456789abcdef0123456789abcdef"}
	owner, server, _ := testdb.Seed(t, db.Raw)
	foreign, otherServer, _ := testdb.Seed(t, db.Raw)
	p := models.AIProvider{UserID: owner, Name: "local fixture", Kind: "compatible", BaseURL: "https://example.invalid", Model: "mock", APIKey: "hidden-provider-key", Enabled: true}
	if err := models.SaveAIProvider(context.Background(), db, &p, true); err != nil {
		t.Fatal(err)
	}
	worker := services.NewAITaskWorker(db, services.NewAIClient(false))
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("db", db)
		user, _ := uuid.Parse(c.GetHeader("X-Fixture-User"))
		c.Set("user_id", user)
	})
	RegisterAITaskRoutes(router.Group("/api"), worker)
	request := func(user uuid.UUID, method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Fixture-User", user.String())
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		return res
	}
	body := map[string]any{"server_id": server, "provider_id": p.ID, "request_id": uuid.New(), "prompt": "Install nginx"}
	res := request(owner, "POST", "/api/ai/tasks", body)
	if res.Code != 202 {
		t.Fatalf("create %d %s", res.Code, res.Body)
	}
	var task models.AITask
	if err := json.Unmarshal(res.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	replay := request(owner, "POST", "/api/ai/tasks", body)
	var same models.AITask
	_ = json.Unmarshal(replay.Body.Bytes(), &same)
	if replay.Code != 202 || same.ID != task.ID {
		t.Fatal("duplicate task created", replay.Code, replay.Body)
	}
	path := "/api/ai/tasks/" + task.ID.String()
	for _, op := range []struct {
		method, path string
		body         any
	}{{"GET", path, nil}, {"POST", path + "/cancel", nil}, {"POST", path + "/messages", map[string]any{"message": "followup", "request_id": uuid.New()}}, {"POST", path + "/actions/" + uuid.NewString() + "/decision", map[string]string{"decision": "approve"}}} {
		response := request(foreign, op.method, op.path, op.body)
		if response.Code != 404 {
			t.Fatalf("foreign %s: %d %s", op.path, response.Code, response.Body)
		}
	}
	response := request(owner, "POST", path+"/actions/"+uuid.NewString()+"/decision", map[string]string{"decision": "approve", "command": "overridden command"})
	if response.Code != 400 {
		t.Fatal("decision accepted replacement command", response.Code)
	}
	response = request(owner, "GET", path, nil)
	if response.Code != 200 || strings.Contains(response.Body.String(), "hidden-provider-key") || strings.Contains(response.Body.String(), "target_fingerprint") || strings.Contains(response.Body.String(), "messages") {
		t.Fatal("private task internals leaked", response.Code, response.Body)
	}
	body["server_id"] = otherServer
	body["request_id"] = uuid.New()
	response = request(owner, "POST", "/api/ai/tasks", body)
	if response.Code != 404 {
		t.Fatal("foreign server accepted", response.Code, response.Body)
	}
	response = request(owner, "POST", path+"/cancel", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"status":"canceled"`) {
		t.Fatal("cancel", response.Code, response.Body)
	}
	// An exhausted completed conversation is a definite rejection, never an
	// ambiguous 500 that would leave clients retrying the same key forever.
	for _, exhausted := range []string{"steps", "context"} {
		steps, messages := 0, `[]`
		if exhausted == "steps" {
			steps = 24
		} else {
			messages = `[{"role":"assistant","content":"` + strings.Repeat("x", 256*1024) + `"}]`
		}
		if _, err := db.Raw.Exec(`UPDATE ai_tasks SET status='completed',steps=$2,messages=$3 WHERE id=$1`, task.ID, steps, messages); err != nil {
			t.Fatal(err)
		}
		response = request(owner, "POST", path+"/messages", map[string]any{"request_id": uuid.New(), "message": "continue"})
		if response.Code != 409 || !strings.Contains(response.Body.String(), "start a new task") {
			t.Fatalf("%s limit: %d %s", exhausted, response.Code, response.Body)
		}
	}
}

func TestAITaskRequestRejectsInvalidPayloadBeforeDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &AITaskHandler{}
	r.POST("/tasks", h.Create)
	for _, body := range []string{`{}`, `{"prompt":"hello","command":"execute me"}`, `{"server_id":"not-an-id"}`, `{} {}`, `{"prompt":"` + strings.Repeat("x", 50000) + `"}`} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/tasks", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(response, request)
		if response.Code != 400 {
			t.Fatal(response.Code)
		}
	}
}
