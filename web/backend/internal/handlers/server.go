package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ServerHandler struct {
	sshCache         *services.SSHConnCache
	collector        *services.Collector
	invalidatePublic func()
}

func NewServerHandler(sshCache *services.SSHConnCache, collector *services.Collector, invalidate ...func()) *ServerHandler {
	h := &ServerHandler{sshCache: sshCache, collector: collector}
	if len(invalidate) > 0 {
		h.invalidatePublic = invalidate[0]
	}
	return h
}

// PollRetry lets the operator retry a failed host immediately instead of
// waiting for the collector's exponential backoff.
func (h *ServerHandler) PollRetry(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if h.collector == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "collector unavailable"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	owned, err := models.ServerOwnedByUser(db.Raw, id, userID)
	if err != nil {
		log.Printf("PollRetry check server %s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load server"})
		return
	}
	if !owned {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	server, err := models.GetServerByIDAndUser(db, id, userID)
	if err != nil {
		// Ownership was established above, so an error here is a decryption,
		// linked-credential, or database failure rather than a foreign ID.
		log.Printf("PollRetry load server %s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load server"})
		return
	}
	if err := h.collector.PollNow(server); err != nil {
		if errors.Is(err, services.ErrPollInFlight) || errors.Is(err, services.ErrPollBusy) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		if errors.Is(err, services.ErrCollectorStopped) {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "collector unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": false, "kind": string(services.ClassifyPollError(err)), "error": services.TrimPollErrorDetail(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type CreateServerRequest struct {
	ConnectionMode    string     `json:"connection_mode"`
	IsPublic          *bool      `json:"is_public"`
	MonitoringEnabled *bool      `json:"monitoring_enabled"`
	AutoRenewEnabled  *bool      `json:"auto_renew_enabled"`
	Name              string     `json:"name"`
	Host              string     `json:"host"`
	Port              int        `json:"port"`
	SSHUsername       string     `json:"ssh_username"`
	SSHPassword       string     `json:"ssh_password"`
	SSHKey            string     `json:"ssh_key"`
	SSHAuthMethod     string     `json:"ssh_auth_method"`
	SSHHostKey        string     `json:"ssh_host_key"`
	CredentialID      *uuid.UUID `json:"credential_id"`
	ServerType        string     `json:"server_type"`
	ExpiresAt         *time.Time `json:"expires_at"`
	Notes             string     `json:"notes"`
	BillingPrice      float64    `json:"billing_price"`
	BillingCurrency   string     `json:"billing_currency"`
	BillingCycle      string     `json:"billing_cycle"`
	TrafficLimit      int64      `json:"traffic_limit_bytes"`
	PublicLocation    string     `json:"public_location"`
}

type UpdateServerRequest struct {
	ConnectionMode    string          `json:"connection_mode"`
	IsPublic          *bool           `json:"is_public"`
	MonitoringEnabled *bool           `json:"monitoring_enabled"`
	AutoRenewEnabled  *bool           `json:"auto_renew_enabled"`
	ExpectedExpiresAt json.RawMessage `json:"expected_expires_at"`
	Name              string          `json:"name" binding:"required"`
	Host              string          `json:"host"`
	Port              int             `json:"port"`
	SSHUsername       string          `json:"ssh_username"`
	SSHPassword       string          `json:"ssh_password"`
	SSHKey            string          `json:"ssh_key"`
	SSHAuthMethod     string          `json:"ssh_auth_method"`
	ClearSSHPassword  bool            `json:"clear_ssh_password"`
	ClearSSHKey       bool            `json:"clear_ssh_key"`
	SSHHostKey        *string         `json:"ssh_host_key"`
	CredentialID      *uuid.UUID      `json:"credential_id"`
	ServerType        string          `json:"server_type"`
	ExpiresAt         *time.Time      `json:"expires_at"`
	Notes             *string         `json:"notes"`
	BillingPrice      float64         `json:"billing_price"`
	BillingCurrency   string          `json:"billing_currency"`
	BillingCycle      string          `json:"billing_cycle"`
	TrafficLimit      int64           `json:"traffic_limit_bytes"`
	PublicLocation    string          `json:"public_location"`
}

func normalizeServerConnection(name, host, username string, port int) (string, string, string, int, error) {
	name = strings.TrimSpace(name)
	host = strings.TrimSpace(host)
	username = strings.TrimSpace(username)

	if name == "" {
		return "", "", "", 0, fmt.Errorf("server name is required")
	}
	if len([]rune(name)) > 128 {
		return "", "", "", 0, fmt.Errorf("server name must be at most 128 characters")
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
	// Attach tags with one query for the whole list instead of one per server.
	ids := make([]uuid.UUID, len(servers))
	for i := range servers {
		ids[i] = servers[i].ID
	}
	tagsByServer, err := models.GetTagsForServers(db.Raw, ids)
	if err != nil {
		log.Printf("List server tags error: %v", err)
	} else {
		for i := range servers {
			if tags := tagsByServer[servers[i].ID]; len(tags) > 0 {
				servers[i].Tags = tags
			}
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
	if req.ConnectionMode == "" {
		req.ConnectionMode = "ssh"
	}
	if req.ConnectionMode != "ssh" && req.ConnectionMode != "agent" {
		c.JSON(400, gin.H{"error": "invalid connection mode"})
		return
	}
	autoName := req.ConnectionMode == "agent" && strings.TrimSpace(req.Name) == ""
	if autoName {
		// CreateServer assigns the placeholder's short ID after allocating its
		// UUID. The ticket, never this display label, binds the installed host.
		req.Name = "Agent"
	}
	if req.ConnectionMode == "agent" {
		req.SSHPassword = ""
		req.SSHKey = ""
		req.SSHHostKey = ""
		req.CredentialID = nil
		if strings.TrimSpace(req.Host) == "" {
			req.Host = "agent"
		}
		if strings.TrimSpace(req.SSHUsername) == "" {
			req.SSHUsername = "agent"
		}
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
	if serverType == "windows" {
		req.SSHKey = ""
	}
	if req.SSHHostKey != "" {
		if err := services.ValidateSSHHostKey(req.SSHHostKey); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
	}
	req.Name, req.Host, req.SSHUsername, req.Port, req.ServerType = name, host, username, port, serverType
	if req.BillingCurrency == "" {
		req.BillingCurrency = "CNY"
	}
	if req.BillingCycle == "" {
		req.BillingCycle = "year"
	}
	db := c.MustGet("db").(*models.DB)
	if req.ConnectionMode == "agent" {
		// Enrollment establishes machine identity; no SSH secret is needed.
	} else if req.CredentialID != nil {
		credential, err := models.GetCredentialByID(db, *req.CredentialID, userID)
		if err != nil || credential.CredType != req.ServerType || credential.SSHAuthMethod == "" || req.ServerType == "windows" && !credential.HasPassword {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential"})
			return
		}
		req.SSHPassword, req.SSHKey = "", ""
		req.SSHAuthMethod = credential.SSHAuthMethod
	} else if req.ServerType == "windows" && strings.TrimSpace(req.SSHPassword) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Windows password is required"})
		return
	} else if req.SSHPassword == "" && strings.TrimSpace(req.SSHKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SSH password or key is required"})
		return
	}
	s := &models.Server{
		AgentAutoName:  autoName,
		ConnectionMode: req.ConnectionMode, IsPublic: boolDefault(req.IsPublic, true), MonitoringEnabled: boolDefault(req.MonitoringEnabled, true), AutoRenewEnabled: boolDefault(req.AutoRenewEnabled, true),
		UserID:          userID,
		Name:            req.Name,
		Host:            req.Host,
		Port:            req.Port,
		SSHUsername:     req.SSHUsername,
		SSHPassword:     req.SSHPassword,
		SSHKey:          req.SSHKey,
		SSHAuthMethod:   req.SSHAuthMethod,
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
	if err := models.CreateServer(db, s, models.SSHAuthUpdate{Method: req.SSHAuthMethod}); err != nil {
		if errors.Is(err, models.ErrSSHAuthentication) {
			c.JSON(400, gin.H{"error": "selected SSH authentication requires a password or key"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create server"})
		return
	}
	if h.invalidatePublic != nil {
		h.invalidatePublic()
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
	db := c.MustGet("db").(*models.DB)
	var mode, actualPlatform, actualUser string
	if err := db.Raw.QueryRowContext(c.Request.Context(), `SELECT connection_mode,COALESCE(server_type,'linux'),ssh_username FROM servers WHERE id=$1 AND user_id=$2`, id, userID).Scan(&mode, &actualPlatform, &actualUser); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(404, gin.H{"error": "server not found"})
		} else {
			c.JSON(500, gin.H{"error": "failed to load server"})
		}
		return
	}
	if req.ConnectionMode != "" && req.ConnectionMode != mode {
		c.JSON(400, gin.H{"error": "connection mode cannot be changed; enroll a new server"})
		return
	}
	if mode == "agent" {
		if req.ServerType != "" && req.ServerType != actualPlatform {
			c.JSON(400, gin.H{"error": "Agent platform is detected on enrollment and cannot be changed here"})
			return
		}
		req.ServerType = actualPlatform
		req.SSHPassword = ""
		req.SSHKey = ""
		emptyKey := ""
		req.SSHHostKey = &emptyKey
		req.CredentialID = nil
		if req.Host == "" {
			req.Host = "agent"
		}
		req.SSHUsername = actualUser
	}
	var expected *models.ExpiryExpectation
	if len(req.ExpectedExpiresAt) > 0 {
		expected = &models.ExpiryExpectation{}
		if err := json.Unmarshal(req.ExpectedExpiresAt, &expected.ExpiresAt); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "expected_expires_at must be an RFC3339 timestamp or null"})
			return
		}
	}
	// Agent users are detected metadata, not SSH login input. Windows service
	// accounts can legitimately contain spaces (NT AUTHORITY\SYSTEM).
	loginForValidation := req.SSHUsername
	if mode == "agent" {
		loginForValidation = "agent"
	}
	name, host, username, port, err := normalizeServerConnection(req.Name, req.Host, loginForValidation, req.Port)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if mode == "agent" {
		username = actualUser
	}
	serverType, err := normalizeServerType(req.ServerType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if serverType == "windows" {
		req.SSHKey = ""
	}
	if req.SSHHostKey != nil && *req.SSHHostKey != "" {
		if err := services.ValidateSSHHostKey(*req.SSHHostKey); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
	}
	req.Name, req.Host, req.SSHUsername, req.Port, req.ServerType = name, host, username, port, serverType
	if req.BillingCurrency == "" {
		req.BillingCurrency = "CNY"
	}
	if req.BillingCycle == "" {
		req.BillingCycle = "year"
	}
	if mode == "agent" {
	} else if req.CredentialID != nil {
		credential, err := models.GetCredentialByID(db, *req.CredentialID, userID)
		if err != nil || credential.CredType != req.ServerType {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid credential"})
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
		CredentialID:    req.CredentialID,
		ServerType:      req.ServerType,
		ExpiresAt:       req.ExpiresAt,
		BillingPrice:    req.BillingPrice,
		BillingCurrency: req.BillingCurrency,
		BillingCycle:    req.BillingCycle,
		TrafficLimit:    req.TrafficLimit,
		PublicLocation:  req.PublicLocation,
	}
	if err := models.UpdateServerWithExpiryExpectation(c.Request.Context(), db, s, expected, models.ServerOptions{IsPublic: req.IsPublic, MonitoringEnabled: req.MonitoringEnabled, AutoRenewEnabled: req.AutoRenewEnabled, Notes: req.Notes, SSHHostKey: req.SSHHostKey, Authentication: models.SSHAuthUpdate{Method: req.SSHAuthMethod, ClearPassword: req.ClearSSHPassword, ClearKey: req.ClearSSHKey}}); err != nil {
		if errors.Is(err, models.ErrSSHAuthentication) {
			c.JSON(400, gin.H{"error": "selected SSH authentication requires a password or key; Windows requires a password"})
			return
		}
		if errors.Is(err, models.ErrServerExpiryChanged) {
			c.JSON(http.StatusConflict, gin.H{"error": "server expiry changed; reload before saving", "error_code": "expiry_changed"})
			return
		}
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update server"})
		return
	}
	s.ConnectionMode = mode
	if h.invalidatePublic != nil {
		h.invalidatePublic()
	}
	if h.sshCache != nil {
		h.sshCache.Drop(id)
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
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete server"})
		return
	}
	if h.invalidatePublic != nil {
		h.invalidatePublic()
	}
	// Close any cached Docker-API connection so nothing stays connected to a
	// host that was just removed from the panel.
	if h.sshCache != nil {
		h.sshCache.Drop(id)
	}
	services.DisconnectAgent(id)
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func boolDefault(value *bool, fallback bool) bool {
	if value != nil {
		return *value
	}
	return fallback
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
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set tags"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "tags updated"})
}
