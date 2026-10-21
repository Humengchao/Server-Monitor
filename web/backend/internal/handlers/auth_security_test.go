package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"server-monitor/internal/config"
	"server-monitor/internal/middleware"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
)

func TestPasswordChangeRevokesTokensAndEstablishedTerminals(t *testing.T) {
	router, db, userID := operationsRouter(t)
	cfg := &config.Config{JWTSecret: "q4N8r7W2a9V6z1L5m0H3k8Y7p2C4s9T6"}
	handler := NewAuthHandler(cfg)
	user, err := models.GetUserByID(db.Raw, userID)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := handler.generateToken(user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	router.POST("/password", middleware.AuthRequired(cfg), handler.ChangePassword)
	router.GET("/protected", middleware.AuthRequired(cfg), func(c *gin.Context) { c.Status(204) })
	closed := make(chan struct{})
	unregister, err := services.RegisterTerminalSession(userID, user.TokensValidAfter, time.Now().Add(time.Hour), func() { close(closed) })
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	body := `{"current_password":"account-password","new_password":"replacement-password"}`
	request := httptest.NewRequest("POST", "/password", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+oldToken)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("password change: %d %s", response.Code, response.Body)
	}
	var replacement struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &replacement); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	default:
		t.Fatal("established terminal survived password change")
	}
	if _, err := services.RegisterTerminalSession(userID, user.TokensValidAfter, time.Now().Add(time.Hour), func() {}); err == nil {
		t.Fatal("stale in-flight terminal registration accepted")
	}
	for _, token := range []string{oldToken, replacement.Token} {
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		result := httptest.NewRecorder()
		router.ServeHTTP(result, req)
		want := http.StatusNoContent
		if token == oldToken {
			want = http.StatusUnauthorized
		}
		if result.Code != want {
			t.Fatalf("token response %d, want %d", result.Code, want)
		}
	}
}

func TestAuthenticationRequiresExpiryIssueTimeAndExactSessionVersion(t *testing.T) {
	router, db, user := operationsRouter(t)
	cfg := &config.Config{JWTSecret: "q4N8r7W2a9V6z1L5m0H3k8Y7p2C4s9T6"}
	version, err := models.GetTokensValidAfter(db.Raw, user)
	if err != nil {
		t.Fatal(err)
	}
	router.GET("/protected", middleware.AuthRequired(cfg), func(c *gin.Context) { c.Status(204) })
	for _, missing := range []string{"exp", "iat", "session_version", "expired", "future", "wrong_version", ""} {
		claims := jwt.MapClaims{"user_id": user.String(), "iat": time.Now().Add(-time.Second).Unix(), "exp": time.Now().Add(time.Hour).Unix(), "session_version": strconv.FormatInt(version.UnixMicro(), 10)}
		switch missing {
		case "expired":
			claims["exp"] = time.Now().Add(-time.Minute).Unix()
		case "future":
			claims["iat"] = time.Now().Add(time.Hour).Unix()
		case "wrong_version":
			claims["session_version"] = "1"
		default:
			delete(claims, missing)
		}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(cfg.JWTSecret))
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("GET", "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		want := 401
		if missing == "" {
			want = 204
		}
		if res.Code != want {
			t.Fatalf("%q got %d, want %d", missing, res.Code, want)
		}
	}
}
