package handlers

import (
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

type DockerHandler struct{}

// checkDockerCached uses the cached has_docker field from the servers table.
// Returns (installed, version) without SSH.
func (h *DockerHandler) checkDockerCached(db *models.DB, serverID uuid.UUID) (bool, string) {
	s, err := models.GetServerByID(db.Raw, serverID)
	if err != nil {
		return false, ""
	}
	return s.HasDocker, s.DockerVersion
}

func NewDockerHandler() *DockerHandler { return &DockerHandler{} }

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
	if _, err := models.GetServerByIDAndUser(db, id, userID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}

	installed, version := h.checkDockerCached(db, id)
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

	client, err := services.DialSSH(server.Host, server.Port, server.SSHUsername, server.SSHPassword, server.SSHKey, server.SSHHostKey)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return
	}
	defer client.Close()

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

	client, err := services.DialSSH(server.Host, server.Port, server.SSHUsername, server.SSHPassword, server.SSHKey, server.SSHHostKey)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return
	}
	defer client.Close()

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

	client, err := services.DialSSH(server.Host, server.Port, server.SSHUsername, server.SSHPassword, server.SSHKey, server.SSHHostKey)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "SSH connection failed"})
		return
	}
	defer client.Close()

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

	client, err := services.DialSSH(server.Host, server.Port, server.SSHUsername, server.SSHPassword, server.SSHKey, server.SSHHostKey)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("SSH connection failed: "+err.Error()))
		return
	}
	defer client.Close()

	// Use shell session (same as SSH terminal), then send docker exec command
	log.Printf("docker exec: starting shell session for container %s", containerID)
	ts, err := services.NewTerminalSession(conn, client)
	if err != nil {
		log.Printf("docker exec: PTY failed: %v", err)
		conn.WriteMessage(websocket.TextMessage, []byte("PTY allocation failed: "+err.Error()))
		return
	}
	defer ts.Close()

	// Wait for shell to initialize, then send docker exec command (try sudo first)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cmd := "sudo docker exec -it " + containerID + " /bin/sh\r"
		log.Printf("docker exec: sending command: %s", cmd)
		ts.Stdin().Write([]byte(cmd))
	}()

	// stdin: websocket → SSH (NewTerminalSession already handles stdout/stderr → websocket via io.Copy)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ts.Read(buf)
			if err != nil {
				return
			}
			if n > 0 {
				ts.Stdin().Write(buf[:n])
			}
		}
	}()

	// Wait for session to complete
	<-ts.Done()
}
