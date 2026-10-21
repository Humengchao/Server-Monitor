package middleware

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"server-monitor/internal/config"
	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// AuthRequired validates JWT from the Authorization header.
func AuthRequired(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString := extractBearerToken(c)
		if tokenString == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization"})
			return
		}
		parseToken(c, tokenString, cfg)
		c.Next()
	}
}

// WSAuthRequired validates JWT for WebSocket upgrades. Browsers cannot set an
// Authorization header on WebSocket handshakes, so the frontend carries the
// token in the subprotocol list ("bearer, <jwt>") where it stays out of URLs
// and access logs. The legacy "token" query parameter is still accepted for
// compatibility.
func WSAuthRequired(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		tokenString := websocketProtocolToken(c.GetHeader("Sec-WebSocket-Protocol"))
		if tokenString == "" {
			tokenString = c.Query("token")
		}
		if tokenString == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing token"})
			return
		}
		parseToken(c, tokenString, cfg)
		c.Next()
	}
}

// websocketProtocolToken extracts the JWT from a "bearer, <jwt>" subprotocol
// offer. JWTs are base64url tokens, which are valid subprotocol names.
func websocketProtocolToken(header string) string {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part != "" && !strings.EqualFold(part, "bearer") {
			return part
		}
	}
	return ""
}

func extractBearerToken(c *gin.Context) string {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return ""
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) == 2 && parts[0] == "Bearer" {
		return parts[1]
	}
	return ""
}

func parseToken(c *gin.Context, tokenString string, cfg *config.Config) {
	if len(tokenString) > 8192 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}
	// Tokens are always issued with HS256; restricting accepted algorithms
	// closes the door on algorithm-confusion attacks.
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return []byte(cfg.JWTSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !token.Valid {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
		return
	}
	issuedAt, issuedErr := claims.GetIssuedAt()
	expiresAt, expiresErr := claims.GetExpirationTime()
	if issuedErr != nil || expiresErr != nil || issuedAt == nil || expiresAt == nil || issuedAt.Unix() <= 0 || !expiresAt.After(issuedAt.Time) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
		return
	}
	userIDClaim, ok := claims["user_id"].(string)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
		return
	}
	userID, err := uuid.Parse(userIDClaim)
	if err != nil || userID == uuid.Nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid user id in token"})
		return
	}
	if !tokenIssuedAfterCutoff(c, userID, claims) {
		return
	}
	username, _ := claims["username"].(string)
	c.Set("user_id", userID)
	c.Set("username", username)
	c.Set("token_issued_at", issuedAt.Time)
	c.Set("token_expires_at", expiresAt.Time)
}

// tokenIssuedAfterCutoff requires the exact password/session version. An iat
// comparison alone cannot distinguish changes within the same second.
func tokenIssuedAfterCutoff(c *gin.Context, userID uuid.UUID, claims jwt.MapClaims) bool {
	raw, exists := c.Get("db")
	db, ok := raw.(*models.DB)
	if !exists || !ok {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return false
	}
	version, ok := claims["session_version"].(string)
	versionMicros, parseErr := strconv.ParseInt(version, 10, 64)
	if !ok || parseErr != nil || versionMicros < 0 {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session expired, please sign in again"})
		return false
	}
	validAfter, err := models.GetTokensValidAfterContext(c.Request.Context(), db.Raw, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid session"})
			return false
		}
		// Fail closed on identity, but not with 401: the client treats that as
		// "log out", and a database blip must not sign everyone out.
		log.Printf("auth: read token cutoff for %s: %v", userID, err)
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authentication temporarily unavailable"})
		return false
	}
	if versionMicros != validAfter.UnixMicro() {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session expired, please sign in again"})
		return false
	}
	c.Set("token_session_version", time.UnixMicro(versionMicros))
	return true
}
