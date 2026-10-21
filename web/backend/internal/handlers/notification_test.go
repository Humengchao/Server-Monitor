package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"server-monitor/internal/models"
	"server-monitor/internal/testdb"
)

func TestNotificationAPIKeepsSecretsAndEnforcesOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	raw := testdb.Open(t)
	db := &models.DB{Raw: raw, EncryptionKey: "01234567890123456789012345678901"}
	user, _, _ := testdb.Seed(t, raw)
	foreign, _, _ := testdb.Seed(t, raw)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		id := user
		if c.GetHeader("X-Test-Foreign") == "true" {
			id = foreign
		}
		c.Set("db", db)
		c.Set("user_id", id)
	})
	RegisterNotificationRoutes(router.Group("/api"))
	request := func(method, path, body string, other bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/notifications"+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if other {
			req.Header.Set("X-Test-Foreign", "true")
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	response := request("POST", "/channels", `{"name":"ops","type":"telegram","enabled":true,"config":{"bot_token":"123:TOP_SECRET","chat_id":"-1001"}}`, false)
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("TOP_SECRET")) {
		t.Fatal("create returned secret")
	}
	var channel models.PublicNotificationChannel
	if err := json.Unmarshal(response.Body.Bytes(), &channel); err != nil {
		t.Fatal(err)
	}
	path := "/channels/" + channel.ID.String()
	// A valid session may edit settings without knowing stored secrets, but it
	// cannot move those secrets to a different destination for exfiltration.
	webhook := request("POST", "/channels", `{"name":"hook","type":"webhook","config":{"webhook_url":"https://original.example/notify","bearer_token":"STORED_TOKEN"}}`, false)
	if webhook.Code != 201 {
		t.Fatal(webhook.Code, webhook.Body.String())
	}
	var hook models.PublicNotificationChannel
	if err := json.Unmarshal(webhook.Body.Bytes(), &hook); err != nil {
		t.Fatal(err)
	}
	moved := request("PUT", "/channels/"+hook.ID.String(), `{"config":{"webhook_url":"https://new.example/notify","bearer_token":""}}`, false)
	if moved.Code != 400 || strings.Contains(moved.Body.String(), "STORED_TOKEN") {
		t.Fatal("destination change retained credential", moved.Code, moved.Body.String())
	}
	unchanged, err := models.GetNotificationChannel(context.Background(), db, hook.ID, user)
	if err != nil || unchanged.Config.WebhookURL != "https://original.example/notify" || unchanged.Config.BearerToken != "STORED_TOKEN" {
		t.Fatal("rejected update changed stored configuration", err)
	}
	response = request("PUT", path, `{"name":"renamed","config":{"bot_token":"","chat_id":"-1002"}}`, false)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	stored, err := models.GetNotificationChannel(context.Background(), db, channel.ID, user)
	if err != nil || stored.Config.BotToken != "123:TOP_SECRET" || stored.Config.ChatID != "-1002" {
		t.Fatal("edit lost saved secret", err)
	}
	response = request("GET", "/channels", "", false)
	if response.Code != 200 || strings.Contains(response.Body.String(), "TOP_SECRET") {
		t.Fatal("list leaked secret")
	}
	response = request("POST", path+"/test", "", false)
	if response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	var queued struct {
		ID int64 `json:"delivery_id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &queued)
	retryPath := "/deliveries/" + strconv.FormatInt(queued.ID, 10) + "/retry"
	if response = request("POST", retryPath, `{"confirm_resend":false}`, false); response.Code != 400 {
		t.Fatal("resend did not require explicit confirmation")
	}
	if response = request("POST", retryPath, `{"confirm_resend":true}`, false); response.Code != 202 {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, attempt := range []struct{ method, path, body string }{
		{http.MethodPut, path, `{"enabled":false}`}, {http.MethodDelete, path, ""}, {http.MethodPost, path + "/test", ""},
		{http.MethodPost, retryPath, `{"confirm_resend":true}`},
	} {
		if response = request(attempt.method, attempt.path, attempt.body, true); response.Code != 404 {
			t.Fatalf("foreign %s: %d %s", attempt.method, response.Code, response.Body.String())
		}
	}
	if response = request("GET", "/deliveries", "", true); response.Code != 200 || strings.Contains(response.Body.String(), "renamed") {
		t.Fatal("foreign delivery leaked")
	}
	if response = request("DELETE", path, "", false); response.Code != 204 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response = request("GET", "/deliveries", "", false); response.Code != 200 || !strings.Contains(response.Body.String(), "canceled") || strings.Contains(response.Body.String(), "TOP_SECRET") {
		t.Fatal("history missing or leaked secret")
	}
}
