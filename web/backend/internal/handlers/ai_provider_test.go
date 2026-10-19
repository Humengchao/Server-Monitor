package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
	"server-monitor/internal/testdb"
)

type fakeAIProviderClient struct {
	validations int
	tests       int
	discoveries int
	err         error
	provider    models.AIProvider
	validate    func(context.Context, string) error
}

func (f *fakeAIProviderClient) ListModels(_ context.Context, p models.AIProvider) ([]services.AIAvailableModel, error) {
	f.discoveries++
	f.provider = p
	return []services.AIAvailableModel{{ID: "new-model", OwnedBy: "fixture"}}, f.err
}

func (f *fakeAIProviderClient) ValidateEndpoint(ctx context.Context, endpoint string) error {
	f.validations++
	if f.validate != nil {
		return f.validate(ctx, endpoint)
	}
	return nil
}
func (f *fakeAIProviderClient) Test(_ context.Context, p models.AIProvider) error {
	f.tests++
	f.provider = p
	return f.err
}

func aiVersionedTestBody(t *testing.T, body string, version time.Time) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatal(err)
	}
	fields["expected_updated_at"] = version
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
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
	router.GET("/providers/:id/models", h.Models)
	versions := map[string]time.Time{}
	request := func(method, path, body string, foreign bool) *httptest.ResponseRecorder {
		if method == "PUT" {
			body = aiVersionedTestBody(t, body, versions[strings.TrimPrefix(path, "/providers/")])
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if foreign {
			req.Header.Set("X-Foreign", "true")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code == 200 || response.Code == 201 {
			var saved models.PublicAIProvider
			if json.Unmarshal(response.Body.Bytes(), &saved) == nil && !saved.UpdatedAt.IsZero() {
				versions[saved.ID.String()] = saved.UpdatedAt
			}
		}
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
	for _, action := range []struct{ method, suffix, body string }{{"PUT", "", `{"name":"foreign"}`}, {"DELETE", "", ""}, {"POST", "/test", ""}, {"GET", "/models", ""}} {
		response = request(action.method, path+action.suffix, action.body, true)
		if response.Code != 404 {
			t.Fatal("foreign provider accessible", response.Code, response.Body.String())
		}
	}
	if client.tests != 0 || client.discoveries != 0 {
		t.Fatal("foreign caller invoked provider")
	}
	response = request("GET", path+"/models", "", false)
	if response.Code != 200 || strings.Contains(response.Body.String(), "private-") || !strings.Contains(response.Body.String(), `"id":"new-model"`) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("model discovery failed or exposed credentials", response.Code, response.Body.String())
	}
	if client.provider.APIKey != "private-replacement" || client.provider.BaseURL != "https://other.example/v1" {
		t.Fatal("model discovery used stale provider")
	}
	client.err = &services.AIProviderError{Code: "models_not_supported"}
	response = request("GET", path+"/models", "", false)
	if response.Code != 502 || !strings.Contains(response.Body.String(), `"code":"models_not_supported"`) {
		t.Fatal("unsupported model catalog not reported", response.Code, response.Body.String())
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
	response = request("GET", path+"/models", "", false)
	if response.Code != 400 || client.discoveries != 2 {
		t.Fatal("disabled provider catalog was sent")
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

func TestAIProviderModelPreviewNeedsExplicitKeyAndDoesNotStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	client := &fakeAIProviderClient{}
	h := &AIProviderHandler{client: client, tests: make(chan struct{}, 4)}
	router := gin.New()
	// No DB is installed: preview must never read or persist credentials.
	router.POST("/models", h.PreviewModels)
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/models", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}
	for _, body := range []string{`{"kind":"openai"}`, `{"kind":"cpa","api_key":"secret"}`, `{"kind":"cpa","base_url":"http://127.0.0.1/v1","api_key":"secret"}`, `{"kind":"openai","api_key":"secret","provider_id":"someone"}`, `{"kind":"openai","api_key":"secret"} {}`} {
		response := request(body)
		if response.Code != 400 || client.discoveries != 0 {
			t.Fatalf("invalid preview reached provider: %d %s", response.Code, response.Body.String())
		}
	}
	response := request(`{"kind":"openai","api_key":"private-preview-key"}`)
	if response.Code != 200 || client.discoveries != 1 || client.provider.BaseURL != "https://api.openai.com/v1" || client.provider.APIKey != "private-preview-key" || strings.Contains(response.Body.String(), "private-preview-key") {
		t.Fatalf("preview failed: %d %s", response.Code, response.Body.String())
	}
	client.err = context.DeadlineExceeded
	response = request(`{"kind":"cpa","base_url":"https://gateway.example/api/v1","api_key":"private-preview-key"}`)
	if response.Code != 504 || !strings.Contains(response.Body.String(), `"code":"timeout"`) {
		t.Fatalf("preview timeout failed: %d %s", response.Code, response.Body.String())
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

func TestAIProviderHTTPConcurrentDisableWinsOverDelayedUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := testdb.Open(t)
	user, _, _ := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw, EncryptionKey: "0123456789abcdef0123456789abcdef"}
	provider := models.AIProvider{UserID: user, Name: "original", Kind: "deepseek", APIKey: "secret", Enabled: true}
	if err := models.SaveAIProvider(context.Background(), db, &provider, true); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	client := &fakeAIProviderClient{validate: func(ctx context.Context, _ string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	h := &AIProviderHandler{client: client}
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("db", db); c.Set("user_id", user) })
	router.PUT("/providers/:id", h.Update)
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/providers/"+provider.ID.String(), strings.NewReader(aiVersionedTestBody(t, body, provider.UpdatedAt)))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(`{"name":"slow edit","enabled":true}`) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("update did not reach validation")
	}
	disabled := request(`{"enabled":false}`)
	close(release)
	if disabled.Code != 200 {
		t.Fatal(disabled.Code, disabled.Body.String())
	}
	select {
	case response := <-done:
		if response.Code != 409 {
			t.Fatal("overlapping edit did not report conflict", response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("delayed edit did not return")
	}
	current, err := models.GetAIProvider(context.Background(), db, provider.ID, user)
	if err != nil || current.Enabled || current.Name != "original" {
		t.Fatal("disable was overwritten", current.Public(), err)
	}
}
