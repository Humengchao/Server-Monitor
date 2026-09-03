package handlers

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"server-monitor/internal/config"
	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	cfg *config.Config
}

func NewAuthHandler(cfg *config.Config) *AuthHandler {
	return &AuthHandler{cfg: cfg}
}

type RegisterRequest struct {
	Username string `json:"username" binding:"required,min=3,max=64"`
	Password string `json:"password" binding:"required,min=6"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}

// minPasswordLength matches the registration rule, so an account can't weaken
// its password below what signup would have accepted.
const minPasswordLength = 6

// tokenTTL bounds how long a single sign-in stays usable.
const tokenTTL = 72 * time.Hour

func (h *AuthHandler) Register(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}
	db := c.MustGet("db").(*models.DB) // will be set in main
	_, err = models.CreateUser(db.Raw, req.Username, string(hash))
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			c.JSON(http.StatusConflict, gin.H{"error": "username already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create user"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "user created"})
}

func (h *AuthHandler) Login(c *gin.Context) {
	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	db := c.MustGet("db").(*models.DB)
	ip := c.ClientIP()
	// Strip port from RemoteAddr if present (e.g. "192.168.1.1:12345" or "[::1]:12345")
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	// Normalize IPv6 loopback to IPv4
	if ip == "::1" {
		ip = "127.0.0.1"
	}
	ua := c.GetHeader("User-Agent")

	user, err := models.GetUserByUsername(db.Raw, req.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		models.InsertLoginRecord(db.Raw, user.ID, ip, ua, false)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}

	// Read the previous sign-in before recording this one, so the response can
	// tell the user where they last came from.
	//
	// A first-ever login legitimately has no prior row: sql.ErrNoRows is the
	// expected answer, not a fault. It used to be logged as "GetLastLogin
	// error" on every new account, alongside a line printing the user's IP on
	// every single login.
	lastLogin, err := models.GetLastLogin(db.Raw, user.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		log.Printf("login: reading previous sign-in for %s: %v", user.ID, err)
	}

	models.InsertLoginRecord(db.Raw, user.ID, ip, ua, true)

	token, err := h.generateToken(user, time.Now())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	resp := gin.H{
		"token": token,
		"user":  gin.H{"id": user.ID, "username": user.Username},
	}

	if lastLogin != nil {
		resp["last_login"] = gin.H{
			"ip":        lastLogin.IP,
			"logged_at": lastLogin.LoggedAt,
		}
	}

	c.JSON(http.StatusOK, resp)
}

func (h *AuthHandler) Me(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	user, err := models.GetUserByID(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": user.ID, "username": user.Username, "created_at": user.CreatedAt})
}

func (h *AuthHandler) LoginHistory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)

	limit := 20
	offset := 0
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	failedOnly := c.Query("failed") == "1"

	records, err := models.GetLoginHistory(db.Raw, userID, limit, offset, failedOnly)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load login history"})
		return
	}
	total, failed, err := models.CountLoginHistory(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load login history"})
		return
	}
	if records == nil {
		records = []models.LoginHistory{}
	}

	// `total` drives pagination, so it has to count the filtered view; `failed`
	// is the unfiltered all-time count that labels the filter itself.
	shown := total
	if failedOnly {
		shown = failed
	}
	c.JSON(http.StatusOK, gin.H{
		"records": records,
		"total":   shown,
		"failed":  failed,
	})
}

// ChangePassword rotates the caller's password and signs every other session
// out. The caller keeps working: the replacement token is minted with exactly
// the revocation cutoff as its issue time.
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.NewPassword) < minPasswordLength {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("the new password must be at least %d characters", minPasswordLength)})
		return
	}
	if req.NewPassword == req.CurrentPassword {
		c.JSON(http.StatusBadRequest, gin.H{"error": "the new password must differ from the current one"})
		return
	}

	db := c.MustGet("db").(*models.DB)
	user, err := models.GetUserByID(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "user not found"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		// 403, not 401: the session itself is perfectly valid, only the supplied
		// password is wrong. A 401 here would tell the client its token is dead
		// and sign the user out for a single typo.
		c.JSON(http.StatusForbidden, gin.H{"error": "current password is incorrect"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 12)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}
	validAfter, err := models.UpdateUserPassword(db.Raw, userID, string(hash))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update password"})
		return
	}

	user.PasswordHash = string(hash)
	token, err := h.generateToken(user, validAfter)
	if err != nil {
		// The password did change; the caller just has to sign in again.
		c.JSON(http.StatusOK, gin.H{"message": "password updated", "reauth_required": true})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "password updated", "token": token})
}

// generateToken mints a JWT. issuedAt is explicit so ChangePassword can align
// it with the revocation cutoff it just wrote.
func (h *AuthHandler) generateToken(user *models.User, issuedAt time.Time) (string, error) {
	claims := jwt.MapClaims{
		"user_id":  user.ID.String(),
		"username": user.Username,
		"iat":      issuedAt.Unix(),
		"exp":      issuedAt.Add(tokenTTL).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(h.cfg.JWTSecret))
}
