package handlers

import (
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ServerHandler struct{}

func NewServerHandler() *ServerHandler { return &ServerHandler{} }

type CreateServerRequest struct {
	Name            string     `json:"name" binding:"required"`
	Host            string     `json:"host" binding:"required"`
	Port            int        `json:"port"`
	SSHUsername     string     `json:"ssh_username"`
	SSHPassword     string     `json:"ssh_password"`
	SSHKey          string     `json:"ssh_key"`
	SSHHostKey      string     `json:"ssh_host_key"`
	CredentialID    *uuid.UUID `json:"credential_id"`
	ServerType      string     `json:"server_type"`
	ExpiresAt       *time.Time `json:"expires_at"`
	Notes           string     `json:"notes"`
	BillingPrice    float64    `json:"billing_price"`
	BillingCurrency string     `json:"billing_currency"`
	BillingCycle    string     `json:"billing_cycle"`
	TrafficLimit    int64      `json:"traffic_limit_bytes"`
	PublicLocation  string     `json:"public_location"`
}

type UpdateServerRequest struct {
	Name            string     `json:"name" binding:"required"`
	Host            string     `json:"host" binding:"required"`
	Port            int        `json:"port"`
	SSHUsername     string     `json:"ssh_username"`
	SSHPassword     string     `json:"ssh_password"`
	SSHKey          string     `json:"ssh_key"`
	SSHHostKey      string     `json:"ssh_host_key"`
	CredentialID    *uuid.UUID `json:"credential_id"`
	ServerType      string     `json:"server_type"`
	ExpiresAt       *time.Time `json:"expires_at"`
	Notes           string     `json:"notes"`
	BillingPrice    float64    `json:"billing_price"`
	BillingCurrency string     `json:"billing_currency"`
	BillingCycle    string     `json:"billing_cycle"`
	TrafficLimit    int64      `json:"traffic_limit_bytes"`
	PublicLocation  string     `json:"public_location"`
}

func normalizeServerConnection(name, host, username string, port int) (string, string, string, int, error) {
	name = strings.TrimSpace(name)
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)

	if name == "" {
		return "", "", "", 0, fmt.Errorf("server name is required")
	}
	if host == "" {
		return "", "", "", 0, fmt.Errorf("host is required")
	}
	// Accept the conventional bracketed IPv6 form in the UI, but store the
	// bare address because net.JoinHostPort adds brackets when dialing.
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if len(host) <= 2 || !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") {
			return "", "", "", 0, fmt.Errorf("host has invalid brackets")
		}
		candidate := host[1 : len(host)-1]
		if net.ParseIP(candidate) == nil {
			return "", "", "", 0, fmt.Errorf("brackets are only valid around an IPv6 address")
		}
		host = candidate
	}
	if strings.Contains(host, "://") {
		return "", "", "", 0, fmt.Errorf("host must not include a URL scheme")
	}
	if strings.ContainsFunc(host, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.Is(unicode.C, r)
	}) {
		return "", "", "", 0, fmt.Errorf("host contains whitespace or invisible characters")
	}
	if net.ParseIP(host) == nil && strings.Contains(host, ":") {
		return "", "", "", 0, fmt.Errorf("host must not include a port")
	}
	if len(host) > 255 {
		return "", "", "", 0, fmt.Errorf("host is too long")
	}
	if username == "" {
		username = "root"
	}
	if strings.ContainsFunc(username, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.Is(unicode.C, r)
	}) {
		return "", "", "", 0, fmt.Errorf("SSH username contains whitespace or control characters")
	}
	if port == 0 {
		port = 22
	}
	if port < 1 || port > 65535 {
		return "", "", "", 0, fmt.Errorf("SSH port must be between 1 and 65535")
	}
	return name, host, username, port, nil
}

func normalizeServerType(serverType string) (string, error) {
	serverType = strings.ToLower(strings.TrimSpace(serverType))
	if serverType == "" {
		return "linux", nil
	}
	if serverType != "linux" && serverType != "windows" {
		return "", fmt.Errorf("server type must be linux or windows")
	}
	return serverType, nil
}

func (h *ServerHandler) List(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	servers, err := models.GetServersByUserID(db.Raw, userID)
	if err != nil {
		log.Printf("List servers error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load servers"})
		return
	}
	if servers == nil {
		servers = []models.Server{}
	}
	// attach tags
	for i := range servers {
		tags, _ := models.GetServerTags(db.Raw, servers[i].ID)
		if tags != nil {
			servers[i].Tags = tags
		}
	}
	c.JSON(http.StatusOK, servers)
}

// Get returns a single server (with latest metrics and tags) by ID.
func (h *ServerHandler) Get(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	s, err := models.GetServerSummary(db.Raw, id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	tags, _ := models.GetServerTags(db.Raw, s.ID)
	if tags != nil {
		s.Tags = tags
	}
	c.JSON(http.StatusOK, s)
}

func (h *ServerHandler) Create(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req CreateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name, host, username, port, err := normalizeServerConnection(req.Name, req.Host, req.SSHUsername, req.Port)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	serverType, err := normalizeServerType(req.ServerType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name, req.Host, req.SSHUsername, req.Port, req.ServerType = name, host, username, port, serverType
	if req.BillingCurrency == "" {
		req.BillingCurrency = "CNY"
	}
	if req.BillingCycle == "" {
		req.BillingCycle = "year"
	}
	db := c.MustGet("db").(*models.DB)
	if req.CredentialID != nil {
		if _, err := models.GetCredentialByID(db, *req.CredentialID, userID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential"})
			return
		}
	} else if req.SSHPassword == "" && strings.TrimSpace(req.SSHKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SSH password or key is required"})
		return
	}
	s := &models.Server{
		UserID:          userID,
		Name:            req.Name,
		Host:            req.Host,
		Port:            req.Port,
		SSHUsername:     req.SSHUsername,
		SSHPassword:     req.SSHPassword,
		SSHKey:          req.SSHKey,
		SSHHostKey:      req.SSHHostKey,
		CredentialID:    req.CredentialID,
		ServerType:      req.ServerType,
		ExpiresAt:       req.ExpiresAt,
		Notes:           req.Notes,
		BillingPrice:    req.BillingPrice,
		BillingCurrency: req.BillingCurrency,
		BillingCycle:    req.BillingCycle,
		TrafficLimit:    req.TrafficLimit,
		PublicLocation:  req.PublicLocation,
	}
	if err := models.CreateServer(db, s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create server"})
		return
	}
	c.JSON(http.StatusCreated, s)
}

func (h *ServerHandler) Update(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req UpdateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name, host, username, port, err := normalizeServerConnection(req.Name, req.Host, req.SSHUsername, req.Port)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	serverType, err := normalizeServerType(req.ServerType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name, req.Host, req.SSHUsername, req.Port, req.ServerType = name, host, username, port, serverType
	if req.BillingCurrency == "" {
		req.BillingCurrency = "CNY"
	}
	if req.BillingCycle == "" {
		req.BillingCycle = "year"
	}
	db := c.MustGet("db").(*models.DB)
	if req.CredentialID != nil {
		if _, err := models.GetCredentialByID(db, *req.CredentialID, userID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential"})
			return
		}
	} else if req.SSHPassword == "" && strings.TrimSpace(req.SSHKey) == "" {
		hasDirectAuth, err := models.ServerHasDirectSSHAuth(db.Raw, id, userID)
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to validate SSH authentication"})
			return
		}
		if !hasDirectAuth {
			c.JSON(http.StatusBadRequest, gin.H{"error": "SSH password or key is required when removing a credential"})
			return
		}
	}
	s := &models.Server{
		ID:              id,
		UserID:          userID,
		Name:            req.Name,
		Host:            req.Host,
		Port:            req.Port,
		SSHUsername:     req.SSHUsername,
		SSHPassword:     req.SSHPassword,
		SSHKey:          req.SSHKey,
		SSHHostKey:      req.SSHHostKey,
		CredentialID:    req.CredentialID,
		ServerType:      req.ServerType,
		ExpiresAt:       req.ExpiresAt,
		Notes:           req.Notes,
		BillingPrice:    req.BillingPrice,
		BillingCurrency: req.BillingCurrency,
		BillingCycle:    req.BillingCycle,
		TrafficLimit:    req.TrafficLimit,
		PublicLocation:  req.PublicLocation,
	}
	if err := models.UpdateServer(db, s); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update server"})
		return
	}
	c.JSON(http.StatusOK, s)
}

func (h *ServerHandler) Delete(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.DeleteServer(db.Raw, id, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete server"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *ServerHandler) SetTags(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		TagIDs []uuid.UUID `json:"tag_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.SetServerTags(db.Raw, id, userID, req.TagIDs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set tags"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "tags updated"})
}
