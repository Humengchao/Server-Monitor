package handlers

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/services"
)

func registerTerminal(c *gin.Context, closeSession func()) (func(), error) {
	version, _ := c.Get("token_session_version")
	expiration, _ := c.Get("token_expires_at")
	sessionVersion, _ := version.(time.Time)
	expiresAt, _ := expiration.(time.Time)
	return services.RegisterTerminalSession(c.MustGet("user_id").(uuid.UUID), sessionVersion, expiresAt, closeSession)
}
