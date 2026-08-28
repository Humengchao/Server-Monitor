package middleware

import (
	"log"
	"net/http"
	"strings"

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
	// Tokens are always issued with HS256; restricting accepted algorithms
	// closes the door on algorithm-confusion attacks.
	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return []byte(cfg.JWTSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		return
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
		return
	}
	userIDClaim, ok := claims["user_id"].(string)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token claims"})
		return
	}
	userID, err := uuid.Parse(userIDClaim)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid user id in token"})
		return
	}
	if !tokenIssuedAfterCutoff(c, userID, claims) {
		return
	}
	username, _ := claims["username"].(string)
	c.Set("user_id", userID)
	c.Set("username", username)
}

// tokenIssuedAfterCutoff rejects tokens minted before the user's last password
// change. This is the one piece of server-side state in an otherwise stateless
// scheme, and it costs a primary-key lookup per request — the price of being
// able to sign other devices out at all.
func tokenIssuedAfterCutoff(c *gin.Context, userID uuid.UUID, claims jwt.MapClaims) bool {
	raw, exists := c.Get("db")
	db, ok := raw.(*models.DB)
	if !exists || !ok {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "database unavailable"})
		return false
	}
	validAfter, err := models.GetTokensValidAfter(db.Raw, userID)
	if err != nil {
		// Fail closed on identity, but not with 401: the client treats that as
		// "log out", and a database blip must not sign everyone out.
		log.Printf("auth: read token cutoff for %s: %v", userID, err)
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "authentication temporarily unavailable"})
		return false
	}
	if validAfter.IsZero() {
		return true
	}
	issuedAt, err := claims.GetIssuedAt()
	if err != nil || issuedAt == nil {
		// Tokens minted before "iat" was added carry no issue time. They expire
		// within the normal 72-hour window, so accept them until then rather
		// than forcing every user to log in again on upgrade.
		return true
	}
	if issuedAt.Time.Before(validAfter) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "session expired, please sign in again"})
		return false
	}
	return true
}
