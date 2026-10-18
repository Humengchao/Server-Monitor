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

func TestPublicVisibilityMutationsInvalidateCachedResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := testdb.Open(t)
	user, id, _ := testdb.Seed(t, raw)
	db := &models.DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	cache := services.NewSSHConnCache()
	defer cache.Close()
	public := NewPublicStatusHandler()
	server := NewServerHandler(cache, nil, public.Invalidate)
	batch := NewBatchHandler(cache, public.Invalidate)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("db", db); c.Set("user_id", user) })
	router.GET("/status", public.Get)
	router.POST("/servers", server.Create)
	router.PUT("/servers/:id", server.Update)
	router.DELETE("/servers/:id", server.Delete)
	router.POST("/bulk/delete", batch.BulkDelete)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		result := httptest.NewRecorder()
		router.ServeHTTP(result, req)
		return result
	}
	count := func(want int) {
		t.Helper()
		response := request("GET", "/status", "")
		var body struct {
			Nodes []json.RawMessage `json:"nodes"`
		}
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || len(body.Nodes) != want {
			t.Fatalf("public count want=%d: %d %s", want, response.Code, response.Body)
		}
	}
	count(1) // populate the public cache before every subsequent mutation
	update := `{"name":"host","host":"192.0.2.1","ssh_username":"root","ssh_password":"fixture","is_public":false}`
	if response := request("PUT", "/servers/"+id.String(), update); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	count(0)
	update = strings.Replace(update, `"is_public":false`, `"is_public":true`, 1)
	if response := request("PUT", "/servers/"+id.String(), update); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	count(1)
	created := request("POST", "/servers", `{"name":"second","host":"192.0.2.2","ssh_password":"fixture","is_public":true}`)
	var added models.Server
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &added) != nil {
		t.Fatal(created.Code, created.Body)
	}
	count(2)
	if response := request("DELETE", "/servers/"+added.ID.String(), ""); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	count(1)
	if response := request("POST", "/bulk/delete", `{"server_ids":["`+id.String()+`"]}`); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	count(0)
}

func TestPublicVisibilityInvalidationPreventsInflightRefill(t *testing.T) {
	h := NewPublicStatusHandler()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.cache.Get(ctx, "public", time.Minute, func(context.Context) (gin.H, error) {
			close(entered)
			<-release
			return gin.H{"visible": "old public server"}, nil
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("refresh did not start")
	}
	h.Invalidate() // equivalent to the mutation's callback after its commit
	close(release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("old refresh did not finish")
	}
	loads := 0
	result, err := h.cache.Get(ctx, "public", time.Minute, func(context.Context) (gin.H, error) { loads++; return gin.H{"visible": "private"}, nil })
	if err != nil || loads != 1 || result["visible"] != "private" {
		t.Fatal("in-flight response refilled invalidated cache", result, loads, err)
	}
	cached, err := h.cache.Get(ctx, "public", time.Minute, func(context.Context) (gin.H, error) { t.Error("fresh value was not cached"); return nil, nil })
	if err != nil || cached["visible"] != "private" {
		t.Fatal(cached, err)
	}
}
