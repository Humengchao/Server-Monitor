package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"server-monitor/internal/agenttransport"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
)

type AgentHandler struct {
	hub               *agenttransport.Hub
	cache             *services.SSHConnCache
	publicURL, assets string
	checksums         *services.RequestCache[string]
}

func NewAgentHandler(hub *agenttransport.Hub, cache *services.SSHConnCache, publicURL, assets string) *AgentHandler {
	return &AgentHandler{hub: hub, cache: cache, publicURL: strings.TrimRight(publicURL, "/"), assets: assets, checksums: services.NewRequestCache[string](1)}
}

func (h *AgentHandler) baseURL(c *gin.Context) (string, error) {
	base := h.publicURL
	if base == "" {
		scheme := "https"
		if c.Request.TLS == nil && c.GetHeader("X-Forwarded-Proto") != "https" {
			scheme = "http"
		}
		base = scheme + "://" + c.Request.Host
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return "", errors.New("configure PUBLIC_URL as the panel HTTPS origin")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")) {
		return "", errors.New("agent enrollment requires HTTPS; configure PUBLIC_URL")
	}
	return base, nil
}

func (h *AgentHandler) Enroll(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid server"})
		return
	}
	base, err := h.baseURL(c)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	ticket, expires, err := models.CreateAgentTicket(c.Request.Context(), c.MustGet("db").(*models.DB).Raw, id, c.MustGet("user_id").(uuid.UUID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(404, gin.H{"error": "agent server not found"})
		} else {
			c.JSON(500, gin.H{"error": "failed to create enrollment"})
		}
		return
	}
	installArgs := "bash \"$installer\" reenroll --server-url " + services.QuoteFileArgument(base) + " --ticket " + services.QuoteFileArgument(ticket)
	command := "(set -e; installer=$(mktemp); trap 'rm -f -- \"$installer\"' EXIT; curl -fsSL " + services.QuoteFileArgument(base+"/api/agent/install.sh") + " -o \"$installer\"; if [ \"$(id -u)\" -eq 0 ]; then " + installArgs + "; else sudo " + installArgs + "; fi)"
	quotePS := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
	windowsCommand := "$installer = Join-Path $env:TEMP ([guid]::NewGuid().ToString() + '.ps1'); try { Invoke-WebRequest -UseBasicParsing " + quotePS(base+"/api/agent/install.ps1") + " -OutFile $installer -ErrorAction Stop; & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $installer -Action reenroll -ServerUrl " + quotePS(base) + " -Ticket " + quotePS(ticket) + "; if ($LASTEXITCODE -ne 0) { throw 'Agent installation failed' } } finally { Remove-Item -LiteralPath $installer -ErrorAction SilentlyContinue }"
	c.JSON(200, gin.H{"server_id": id, "expires_at": expires, "command": command, "windows_command": windowsCommand, "ticket": ticket, "server_url": base})
}

func (h *AgentHandler) Register(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var request agenttransport.RegisterRequest
	if c.ShouldBindJSON(&request) != nil {
		c.JSON(400, gin.H{"error": "invalid enrollment"})
		return
	}
	id, token, err := models.RegisterAgent(c.Request.Context(), c.MustGet("db").(*models.DB).Raw, request.Ticket, request.Hello)
	if err != nil {
		c.JSON(403, gin.H{"error": "enrollment rejected; generate a new installation command"})
		return
	}
	h.hub.Disconnect(id)
	if h.cache != nil {
		h.cache.Drop(id)
	}
	c.JSON(200, agenttransport.RegisterResponse{AgentID: id.String(), ServerID: id.String(), Token: token})
}

func (h *AgentHandler) authenticate(c *gin.Context) (uuid.UUID, string, bool) {
	raw := c.GetHeader("Authorization")
	token, ok := strings.CutPrefix(raw, "Bearer ")
	if !ok {
		c.JSON(401, gin.H{"error": "agent authentication required"})
		return uuid.Nil, "", false
	}
	id, key, err := models.AuthenticateAgent(c.Request.Context(), c.MustGet("db").(*models.DB).Raw, token)
	if err != nil {
		c.JSON(401, gin.H{"error": "agent authentication failed"})
		return uuid.Nil, "", false
	}
	return id, key, true
}

var agentUpgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}

func (h *AgentHandler) Control(c *gin.Context) {
	id, key, ok := h.authenticate(c)
	if !ok {
		return
	}
	ws, err := agentUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	db := c.MustGet("db").(*models.DB)
	ctx, cancel := context.WithCancel(c.Request.Context())
	defer cancel()
	_ = h.hub.ServeControl(ctx, id, ws, func(hello agenttransport.Hello) error {
		token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		currentID, currentKey, err := models.AuthenticateAgent(ctx, db.Raw, token)
		if err != nil || currentID != id || currentKey != key {
			return errors.New("agent access was revoked")
		}
		if strings.TrimSpace(hello.HostKey) != strings.TrimSpace(key) {
			return errors.New("agent identity changed; reenroll")
		}
		return models.TouchAgent(ctx, db.Raw, id, hello)
	})
}
func (h *AgentHandler) Data(c *gin.Context) {
	id, _, ok := h.authenticate(c)
	if !ok {
		return
	}
	session := c.Query("session")
	if len(session) > 128 || session == "" {
		c.JSON(400, gin.H{"error": "invalid session"})
		return
	}
	ws, err := agentUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	_ = h.hub.AttachData(id, session, ws)
}
func (h *AgentHandler) Status(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid server"})
		return
	}
	status, err := models.GetAgentStatus(c.Request.Context(), c.MustGet("db").(*models.DB).Raw, id, c.MustGet("user_id").(uuid.UUID))
	if err != nil {
		c.JSON(404, gin.H{"error": "agent server not found"})
		return
	}
	status.Online = h.hub.Online(id)
	if status.Online {
		now := time.Now().UTC()
		status.LastSeenAt = &now
	}
	c.JSON(200, status)
}
func (h *AgentHandler) Revoke(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid server"})
		return
	}
	if err := models.RevokeAgent(c.Request.Context(), c.MustGet("db").(*models.DB).Raw, id, c.MustGet("user_id").(uuid.UUID)); err != nil {
		c.JSON(404, gin.H{"error": "agent server not found"})
		return
	}
	h.hub.Disconnect(id)
	if h.cache != nil {
		h.cache.Drop(id)
	}
	c.Status(204)
}
func (h *AgentHandler) Download(c *gin.Context) {
	platform, arch := c.Param("os"), c.Param("arch")
	if (platform != "linux" && platform != "windows") || (arch != "amd64" && arch != "arm64") {
		c.Status(404)
		return
	}
	name := "server-monitor-agent-" + platform + "-" + arch
	if platform == "windows" {
		name += ".exe"
	}
	path := filepath.Join(h.assets, name)
	file, err := os.Open(path)
	if err != nil {
		c.JSON(503, gin.H{"error": "agent binary is not available for this platform"})
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		c.Status(http.StatusServiceUnavailable)
		return
	}
	if c.Query("checksum") == "1" {
		key := fmt.Sprintf("%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
		sum, err := h.checksums.Get(c.Request.Context(), key, 24*time.Hour, func(ctx context.Context) (string, error) {
			input, err := os.Open(path)
			if err != nil {
				return "", err
			}
			defer input.Close()
			before, err := input.Stat()
			if err != nil {
				return "", err
			}
			if !os.SameFile(info, before) || info.Size() != before.Size() || !info.ModTime().Equal(before.ModTime()) {
				return "", errors.New("agent binary changed")
			}
			hash := sha256.New()
			buffer := make([]byte, 64*1024)
			for {
				if err := ctx.Err(); err != nil {
					return "", err
				}
				n, readErr := input.Read(buffer)
				if n > 0 {
					_, _ = hash.Write(buffer[:n])
				}
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return "", readErr
				}
			}
			after, err := input.Stat()
			if err != nil {
				return "", err
			}
			if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
				return "", errors.New("agent binary changed")
			}
			return hex.EncodeToString(hash.Sum(nil)), nil
		})
		if err != nil {
			c.Status(http.StatusServiceUnavailable)
			return
		}
		c.Data(200, "text/plain", []byte(sum+"  "+name+"\n"))
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Header("Content-Type", "application/octet-stream")
	http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
}
func (h *AgentHandler) Installer(c *gin.Context) {
	name := "install.sh"
	if strings.HasSuffix(c.Request.URL.Path, ".ps1") {
		name = "install.ps1"
	}
	data, err := os.ReadFile(filepath.Join(h.assets, name))
	if err != nil {
		c.JSON(503, gin.H{"error": "agent installer unavailable"})
		return
	}
	c.Data(200, "text/plain; charset=utf-8", data)
}
