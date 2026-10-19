package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
	"server-monitor/internal/testdb"
)

type aiExtensionFixtureClient struct {
	validations, lists int
	err                error
	validate           func()
}

func (f *aiExtensionFixtureClient) ValidateEndpoint(context.Context, string) error {
	f.validations++
	if f.validate != nil {
		f.validate()
	}
	return nil
}
func (f *aiExtensionFixtureClient) ListTools(context.Context, models.AIPlugin) ([]services.AIMCPTool, error) {
	f.lists++
	return []services.AIMCPTool{{Name: "fixture_tool", Description: "readOnlyHint cannot grant approval", InputSchema: json.RawMessage(`{"type":"object"}`)}}, f.err
}

func TestAIExtensionsHTTPCRUDOwnershipCredentialsAndDiscovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := testdb.Open(t)
	user, _, _ := testdb.Seed(t, raw)
	foreign, _, _ := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw, EncryptionKey: "0123456789abcdef0123456789abcdef"}
	client := &aiExtensionFixtureClient{}
	h := &AIExtensionHandler{client: client, requests: make(chan struct{}, 4)}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		owner := user
		if c.GetHeader("X-Foreign") == "true" {
			owner = foreign
		}
		c.Set("db", db)
		c.Set("user_id", owner)
	})
	router.GET("/skills", h.ListSkills)
	router.GET("/skills/:id", h.GetSkill)
	router.POST("/skills", h.CreateSkill)
	router.PUT("/skills/:id", h.UpdateSkill)
	router.DELETE("/skills/:id", h.DeleteSkill)
	router.GET("/plugins", h.ListPlugins)
	router.POST("/plugins", h.CreatePlugin)
	router.PUT("/plugins/:id", h.UpdatePlugin)
	router.DELETE("/plugins/:id", h.DeletePlugin)
	router.POST("/plugins/:id/test", h.TestPlugin)
	router.GET("/plugins/:id/tools", h.PluginTools)
	request := func(method, path, body string, foreign bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if foreign {
			req.Header.Set("X-Foreign", "true")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	response := request("POST", "/skills", `{"name":"Operations","description":"notes","content":"# First inspect\nThen configure."}`, false)
	var skill models.AISkill
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &skill) != nil || skill.Content != "# First inspect\nThen configure." {
		t.Fatal(response.Code, response.Body.String())
	}
	skillPath := "/skills/" + skill.ID.String()
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		if got := request(method, skillPath, `{"content":"foreign"}`, true); got.Code != 404 {
			t.Fatal("foreign skill access", got.Code)
		}
	}
	if got := request("GET", "/skills", "", true); got.Body.String() != "[]" {
		t.Fatal("foreign skill list", got.Body.String())
	}
	if got := request("PUT", skillPath, `{"content":"# Updated"}`, false); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	tooLarge, _ := json.Marshal(map[string]string{"name": "large", "content": strings.Repeat("x", 32769)})
	if got := request("POST", "/skills", string(tooLarge), false); got.Code != 400 {
		t.Fatal("oversized skill accepted")
	}
	for _, body := range []string{`{"name":"bad","content":"text","user_id":"other"}`, `{"name":"bad","content":"text"} {}`} {
		if got := request("POST", "/skills", body, false); got.Code != 400 {
			t.Fatal("invalid body accepted")
		}
	}
	response = request("POST", "/plugins", `{"name":"Fixture","url":"https://example.com/mcp","bearer_token":"private-fixture-token"}`, false)
	var plugin models.PublicAIPlugin
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &plugin) != nil || !plugin.HasBearerToken || strings.Contains(response.Body.String(), "private-fixture-token") {
		t.Fatal(response.Code, response.Body.String())
	}
	pluginPath := "/plugins/" + plugin.ID.String()
	if got := request("PUT", pluginPath, `{"name":"Renamed","bearer_token":""}`, false); got.Code != 200 || strings.Contains(got.Body.String(), "private-fixture-token") {
		t.Fatal(got.Body.String())
	}
	before := client.validations
	if got := request("PUT", pluginPath, `{"url":"https://other.example/mcp"}`, false); got.Code != 400 || client.validations != before {
		t.Fatal("new endpoint accepted old token", got.Code)
	}
	for _, action := range []struct{ method, suffix string }{{"PUT", ""}, {"DELETE", ""}, {"POST", "/test"}, {"GET", "/tools"}} {
		if got := request(action.method, pluginPath+action.suffix, `{"name":"foreign"}`, true); got.Code != 404 {
			t.Fatal("foreign plugin access", got.Code)
		}
	}
	if client.lists != 0 {
		t.Fatal("foreign tools request reached server")
	}
	if got := request("GET", "/plugins", "", true); got.Body.String() != "[]" {
		t.Fatal("foreign plugin list")
	}
	if got := request("GET", pluginPath+"/tools", "", false); got.Code != 200 || !strings.Contains(got.Body.String(), `"inputSchema"`) || !strings.Contains(got.Body.String(), `"fetched_at"`) {
		t.Fatal(got.Body.String())
	}
	if got := request("POST", pluginPath+"/test", "", false); got.Code != 200 || !strings.Contains(got.Body.String(), `"tool_count":1`) {
		t.Fatal(got.Body.String())
	}
	client.err = errors.New("private-fixture-token https://internal.invalid")
	if got := request("POST", pluginPath+"/test", "", false); got.Code != 502 || strings.Contains(got.Body.String(), "private-fixture-token") || strings.Contains(got.Body.String(), "internal.invalid") {
		t.Fatal("unsafe upstream error", got.Body.String())
	}
	client.err = nil
	if got := request("PUT", pluginPath, `{"url":"https://other.example/mcp","clear_token":true}`, false); got.Code != 200 || strings.Contains(got.Body.String(), `"has_bearer_token":true`) {
		t.Fatal("explicit clear", got.Body.String())
	}
	// A concurrent disable while endpoint validation is in flight must win.
	client.validate = func() {
		current, err := models.GetAIPlugin(context.Background(), db, plugin.ID, user)
		if err != nil {
			t.Fatal(err)
		}
		current.Enabled = false
		current.BearerToken = ""
		if err := models.SaveAIPlugin(context.Background(), db, &current, false, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := request("PUT", pluginPath, `{"enabled":true}`, false); got.Code != 409 {
		t.Fatal("concurrent disable overwritten", got.Body.String())
	}
	client.validate = nil
	before = client.lists
	if got := request("POST", pluginPath+"/test", "", false); got.Code != 400 || client.lists != before {
		t.Fatal("disabled plugin called", got.Body.String())
	}
	for _, path := range []string{skillPath, pluginPath} {
		if got := request("DELETE", path, "", false); got.Code != 204 {
			t.Fatal(got.Code, got.Body.String())
		}
	}
}
