package handlers

import (
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

	// Get last login BEFORE inserting the new record
	lastLogin, _ := models.GetLastLogin(db.Raw, user.ID)

	models.InsertLoginRecord(db.Raw, user.ID, ip, ua, true)

	token, err := h.generateToken(user)
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

	records, err := models.GetLoginHistory(db.Raw, userID, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load login history"})
		return
	}
	total, _ := models.CountLoginHistory(db.Raw, userID)
	if records == nil {
		records = []models.LoginHistory{}
	}

	c.JSON(http.StatusOK, gin.H{
		"records": records,
		"total":   total,
	})
}

func (h *AuthHandler) generateToken(user *models.User) (string, error) {
	claims := jwt.MapClaims{
		"user_id":  user.ID.String(),
		"username": user.Username,
		"exp":      time.Now().Add(72 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(h.cfg.JWTSecret))
}
