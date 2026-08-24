package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// validDockerID matches Docker container IDs (hex, 12-64 chars)
var validDockerID = regexp.MustCompile(`^[a-fA-F0-9]{1,64}$`)

// DockerHandler serves its short commands (list, action, logs) over cached
// SSH connections so every click doesn't pay a full handshake. Interactive
// exec sessions dial their own connection: they are long-lived and their
// teardown closes the client.
type DockerHandler struct {
	sshCache *services.SSHConnCache
}

func NewDockerHandler(sshCache *services.SSHConnCache) *DockerHandler {
	return &DockerHandler{sshCache: sshCache}
}

type DockerContainer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	State   string `json:"state"`
	Ports   string `json:"ports"`
	Created string `json:"created"`
}

func (h *DockerHandler) CheckDocker(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	installed, version, err := models.GetServerDockerInfo(db.Raw, id, userID)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check docker"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"installed": installed,
		"version":   version,
	})
}

func (h *DockerHandler) ListContainers(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	server, err := models.GetServerByIDAndUser(db, id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	client, err := h.sshCache.Get(server)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return
	}

	output, err := services.RunDockerCmd(client, `ps -a --format '{"id":"{{.ID}}","name":"{{.Names}}","image":"{{.Image}}","status":"{{.Status}}","state":"{{.State}}","ports":"{{.Ports}}","created":"{{.CreatedAt}}"}'`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list containers"})
		return
	}

	var containers []DockerContainer
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var dc DockerContainer
		if err := json.Unmarshal([]byte(line), &dc); err == nil {
			containers = append(containers, dc)
		}
	}
	if containers == nil {
		containers = []DockerContainer{}
	}

	c.JSON(http.StatusOK, containers)
}

func (h *DockerHandler) ContainerAction(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	containerID := c.Param("containerId")
	action := c.Param("action")

	if !validDockerID.MatchString(containerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid container id"})
		return
	}
	if action != "start" && action != "stop" && action != "restart" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid action"})
		return
	}

	db := c.MustGet("db").(*models.DB)
	server, err := models.GetServerByIDAndUser(db, id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	client, err := h.sshCache.Get(server)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return
	}

	_, err = services.RunDockerCmd(client, action+" "+containerID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "action failed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ok"})
}

func (h *DockerHandler) ContainerLogs(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	containerID := c.Param("containerId")
	if !validDockerID.MatchString(containerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid container id"})
		return
	}
	tail := c.DefaultQuery("tail", "200")
	tailNum, err := strconv.Atoi(tail)
	if err != nil || tailNum < 1 || tailNum > 10000 {
		tailNum = 200
	}

	db := c.MustGet("db").(*models.DB)
	server, err := models.GetServerByIDAndUser(db, id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	client, err := h.sshCache.Get(server)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return
	}

	cmd := fmt.Sprintf("logs --tail %d %s", tailNum, containerID)
	output, err := services.RunDockerCmd(client, cmd)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get logs"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"logs": output})
}

func (h *DockerHandler) ContainerExec(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	containerID := c.Param("containerId")

	if !validDockerID.MatchString(containerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid container id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	server, err := models.GetServerByIDAndUser(db, id, userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	defer conn.Close()

	// Exec sessions intentionally bypass the connection cache: they live as
	// long as the websocket and TerminalSession.Close closes the whole client,
	// which would kill a shared cached connection for everyone else.
	client, err := services.DialSSH(server.Host, server.Port, server.SSHUsername, server.SSHPassword, server.SSHKey, server.SSHHostKey)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("SSH connection failed: "+err.Error()))
		return
	}
	defer client.Close()

	// Use shell session (same as SSH terminal), then send docker exec command
	ts, err := services.NewTerminalSession(conn, client)
	if err != nil {
		log.Printf("docker exec: PTY failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("PTY allocation failed: "+err.Error()))
		return
	}
	defer ts.Close()

	// Wait for shell to initialize, then start docker exec.
	// Only use sudo when docker isn't usable without it, and prefer bash
	// inside the container, falling back to sh.
	go func() {
		time.Sleep(500 * time.Millisecond)
		shell := "sh -c 'command -v bash >/dev/null && exec bash || exec sh'"
		cmd := fmt.Sprintf(
			"if docker ps >/dev/null 2>&1; then docker exec -it %s %s; else sudo docker exec -it %s %s; fi\r",
			containerID, shell, containerID, shell)
		ts.Stdin().Write([]byte(cmd))
	}()

	// stdin: websocket → SSH, including resize control messages
	// (NewTerminalSession already handles stdout/stderr → websocket)
	go ts.PumpStdin()

	// Wait for session to complete (client disconnect or shell exit)
	<-ts.Done()
}
