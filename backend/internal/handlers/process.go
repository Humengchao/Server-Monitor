package handlers

import (
	"net/http"
	"strconv"

	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
)

// ProcessHandler reads the remote process table over the shared SSH connection
// cache, so polling the list doesn't pay a handshake per refresh.
type ProcessHandler struct {
	sshCache *services.SSHConnCache
}

func NewProcessHandler(sshCache *services.SSHConnCache) *ProcessHandler {
	return &ProcessHandler{sshCache: sshCache}
}

func (h *ProcessHandler) List(c *gin.Context) {
	server, ok := resolveOwnedServer(c)
	if !ok {
		return
	}
	client, err := h.sshCache.Get(server)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return
	}

	var procs []services.ProcessInfo
	if server.ServerType == "windows" {
		procs, err = services.ListWindowsProcesses(client)
	} else {
		procs, err = services.ListLinuxProcesses(client)
	}
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to read the process list"})
		return
	}

	total := len(procs)
	procs = services.TopProcesses(procs, services.MaxProcesses)
	if procs == nil {
		procs = []services.ProcessInfo{}
	}
	c.JSON(http.StatusOK, gin.H{
		"processes": procs,
		// total lets the UI say "showing 300 of 812" instead of silently
		// pretending the truncated list is everything.
		"total":    total,
		"returned": len(procs),
	})
}

func (h *ProcessHandler) Kill(c *gin.Context) {
	pid, err := strconv.Atoi(c.Param("pid"))
	if err != nil || pid <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid pid"})
		return
	}
	server, ok := resolveOwnedServer(c)
	if !ok {
		return
	}
	client, err := h.sshCache.Get(server)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return
	}
	// force=true escalates SIGTERM to SIGKILL; on Windows Stop-Process is
	// forceful either way.
	if err := services.KillProcess(client, pid, server.ServerType, c.Query("force") == "1"); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "signal sent"})
}
