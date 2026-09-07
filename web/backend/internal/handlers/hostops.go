package handlers

import (
	"errors"
	"net/http"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// resolveOwnedServer loads a server the caller owns. It writes the error
// response itself and returns ok=false when the id is malformed or the server
// belongs to someone else — the two are answered identically on purpose, so the
// endpoint cannot be used to probe which ids exist.
func resolveOwnedServer(c *gin.Context) (*models.Server, bool) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return nil, false
	}
	db := c.MustGet("db").(*models.DB)
	server, err := models.GetServerByIDAndUser(db, id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return nil, false
	}
	return server, true
}

// HostOpsHandler serves the service inventory and the listening-port list. Both
// go over the shared SSH connection cache, so polling them costs a session
// rather than a handshake.
type HostOpsHandler struct {
	sshCache *services.SSHConnCache
}

func NewHostOpsHandler(sshCache *services.SSHConnCache) *HostOpsHandler {
	return &HostOpsHandler{sshCache: sshCache}
}

// connect resolves the server and opens a pooled client, writing its own error
// response on failure.
func (h *HostOpsHandler) connect(c *gin.Context) (*models.Server, *ssh.Client, bool) {
	server, ok := resolveOwnedServer(c)
	if !ok {
		return nil, nil, false
	}
	client, err := h.sshCache.Get(server)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return nil, nil, false
	}
	return server, client, true
}

func (h *HostOpsHandler) ListServices(c *gin.Context) {
	server, client, ok := h.connect(c)
	if !ok {
		return
	}

	var (
		units   []services.ServiceUnit
		manager services.ServiceManager
		err     error
	)
	if server.ServerType == "windows" {
		units, err = services.ListWindowsServices(client)
		manager = services.ManagerWindows
	} else {
		units, manager, err = services.ListLinuxServices(client)
	}
	if services.ServiceInventoryUnavailable(err) {
		// Not an API failure: the host genuinely cannot be asked, which is normal
		// inside a container or for an unprivileged SSH user. Reporting it as a
		// state lets the UI explain itself instead of showing a generic error.
		c.JSON(http.StatusOK, gin.H{
			"services": []services.ServiceUnit{}, "total": 0, "returned": 0,
			"supported": false,
			// The code is what the UI renders; the sentence exists for API
			// consumers and logs, and is only ever written in English.
			"reason_code": serviceUnavailableCode(err),
			"reason":      err.Error(),
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read the service list"})
		return
	}

	total := len(units)
	services.SortServiceUnits(units)
	if len(units) > services.MaxServiceUnits {
		units = units[:services.MaxServiceUnits]
	}
	if units == nil {
		units = []services.ServiceUnit{}
	}
	c.JSON(http.StatusOK, gin.H{
		"services": units,
		"manager":  manager,
		// total lets the UI say "showing 500 of 620" rather than presenting a
		// truncated list as the whole inventory.
		"total":     total,
		"returned":  len(units),
		"supported": true,
	})
}

// serviceUnavailableCode maps a host state onto a stable token, so the UI can
// say why in the reader's own language instead of echoing an English sentence
// into a localized page.
func serviceUnavailableCode(err error) string {
	if errors.Is(err, services.ErrServiceManagerUnreachable) {
		return "unreachable"
	}
	return "absent"
}

type serviceControlRequest struct {
	Name   string `json:"name" binding:"required"`
	Action string `json:"action" binding:"required"`
}

func (h *HostOpsHandler) ControlService(c *gin.Context) {
	var req serviceControlRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name and action are required"})
		return
	}
	// Validated before opening a connection: a rejected name should not cost an
	// SSH session, and the message must not vary with whether the host is up.
	if !services.ServiceActions[req.Action] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported action"})
		return
	}
	if !services.ValidServiceName(req.Name) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported service name"})
		return
	}

	server, client, ok := h.connect(c)
	if !ok {
		return
	}
	if err := services.ControlService(client, req.Name, req.Action, server.ServerType); err != nil {
		// The host's own refusal ("Access denied", "Unit not found") is far more
		// useful than a generic message, and it is the host talking, not us.
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "command sent"})
}

func (h *HostOpsHandler) ListPorts(c *gin.Context) {
	server, client, ok := h.connect(c)
	if !ok {
		return
	}

	var (
		ports []services.ListeningPort
		err   error
	)
	if server.ServerType == "windows" {
		ports, err = services.ListWindowsPorts(client)
	} else {
		ports, err = services.ListLinuxPorts(client)
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read the listening ports"})
		return
	}

	total := len(ports)
	services.SortListeningPorts(ports)
	if len(ports) > services.MaxListeningPorts {
		ports = ports[:services.MaxListeningPorts]
	}
	if ports == nil {
		ports = []services.ListeningPort{}
	}
	c.JSON(http.StatusOK, gin.H{
		"ports": ports, "total": total, "returned": len(ports),
	})
}
