package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
	"server-monitor/internal/testdb"
)

type fakeAIProviderClient struct {
	validations int
	tests       int
	err         error
	provider    models.AIProvider
}

func (f *fakeAIProviderClient) ValidateEndpoint(context.Context, string) error {
	f.validations++
	return nil
}
func (f *fakeAIProviderClient) Test(_ context.Context, p models.AIProvider) error {
	f.tests++
	f.provider = p
	return f.err
}

func TestAIProviderHTTPKeyRetentionOwnershipAndSafeUpstreamErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := testdb.Open(t)
	user, _, _ := testdb.Seed(t, raw)
	other, _, _ := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw, EncryptionKey: "0123456789abcdef0123456789abcdef"}
	client := &fakeAIProviderClient{}
	h := &AIProviderHandler{client: client, tests: make(chan struct{}, 4)}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		owner := user
		if c.GetHeader("X-Foreign") == "true" {
			owner = other
		}
		c.Set("user_id", owner)
		c.Set("db", db)
	})
	router.GET("/providers", h.List)
	router.POST("/providers", h.Create)
	router.PUT("/providers/:id", h.Update)
	router.DELETE("/providers/:id", h.Delete)
	router.POST("/providers/:id/test", h.Test)
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
	response := request("POST", "/providers", `{"name":"DeepSeek","kind":"deepseek","api_key":"private-original"}`, false)
	if response.Code != 201 || strings.Contains(response.Body.String(), "private-original") {
		t.Fatal(response.Code, response.Body.String())
	}
	var public models.PublicAIProvider
	if err := json.Unmarshal(response.Body.Bytes(), &public); err != nil {
		t.Fatal(err)
	}
	if !public.HasAPIKey || !public.Enabled || public.Model != models.DefaultDeepSeekModel || public.BaseURL != models.DefaultDeepSeekBaseURL {
		t.Fatal("wrong public defaults", public)
	}
	path := "/providers/" + public.ID.String()
	response = request("PUT", path, `{"name":"Renamed","api_key":""}`, false)
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-original") {
		t.Fatal(response.Code, response.Body.String())
	}
	saved, err := models.GetAIProvider(context.Background(), db, public.ID, user)
	if err != nil || saved.APIKey != "private-original" {
		t.Fatal("blank update lost key", err)
	}
	for _, base := range []string{"https://other.example/v1", models.DefaultDeepSeekBaseURL + "/v1"} {
		response = request("PUT", path, `{"base_url":"`+base+`"}`, false)
		if response.Code != 400 {
			t.Fatal("changed endpoint accepted without key", response.Code, response.Body.String())
		}
	}
	response = request("PUT", path, `{"base_url":"https://other.example/v1","api_key":"private-replacement"}`, false)
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-replacement") {
		t.Fatal(response.Code, response.Body.String())
	}
	response = request("GET", "/providers", "", false)
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-") {
		t.Fatal("list leaked key", response.Body.String())
	}
	response = request("GET", "/providers", "", true)
	if response.Code != 200 || response.Body.String() != "[]" {
		t.Fatal("foreign list leaked provider", response.Body.String())
	}
	for _, action := range []struct{ method, suffix, body string }{{"PUT", "", `{"name":"foreign"}`}, {"DELETE", "", ""}, {"POST", "/test", ""}} {
		response = request(action.method, path+action.suffix, action.body, true)
		if response.Code != 404 {
			t.Fatal("foreign provider accessible", response.Code, response.Body.String())
		}
	}
	if client.tests != 0 {
		t.Fatal("foreign caller invoked provider")
	}
	client.err = &services.AIProviderError{Code: "authentication_failed"}
	response = request("POST", path+"/test", "", false)
	if response.Code != 502 || strings.Contains(response.Body.String(), "private-") || !strings.Contains(response.Body.String(), `"code":"authentication_failed"`) {
		t.Fatal("unsafe upstream auth handling", response.Code, response.Body.String())
	}
	if client.provider.APIKey != "private-replacement" || client.provider.BaseURL != "https://other.example/v1" {
		t.Fatal("test used stale provider")
	}
	client.err = nil
	response = request("POST", path+"/test", "", false)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatal(response.Code, response.Body.String())
	}
	response = request("PUT", path, `{"enabled":false}`, false)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = request("POST", path+"/test", "", false)
	if response.Code != 400 || client.tests != 2 {
		t.Fatal("disabled provider was sent")
	}
	response = request("DELETE", path, "", false)
	if response.Code != 204 {
		t.Fatal(response.Code, response.Body.String())
	}
	response = request("DELETE", path, "", false)
	if response.Code != 404 {
		t.Fatal("missing delete succeeded")
	}
}

func TestAIProviderHTTPInputRejectedBeforeDatabaseOrNetwork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := &fakeAIProviderClient{}
	h := &AIProviderHandler{client: client}
	router := gin.New()
	router.POST("/providers", h.Create)
	for _, body := range []string{`{"name":"test","unexpected":"secret"}`, `{"api_key":42}`, `{} {}`, strings.Repeat("x", 16385)} {
		req := httptest.NewRequest("POST", "/providers", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != 400 {
			t.Fatal("invalid request reached database", response.Code)
		}
	}
	if client.tests != 0 || client.validations != 0 {
		t.Fatal("malformed request invoked network")
	}
}
