package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"server-monitor/internal/config"

	"github.com/gin-gonic/gin"
)

func TestRegisterDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAuthHandler(&config.Config{AllowRegistration: false})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/register",
		strings.NewReader(`{"username":"newuser","password":"secret123"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Register(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("Register with AllowRegistration=false: status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if !strings.Contains(w.Body.String(), "disabled") {
		t.Fatalf("Register with AllowRegistration=false: body = %q, want an explanatory error", w.Body.String())
	}
}
