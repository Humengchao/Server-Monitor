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
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Image            string  `json:"image"`
	Status           string  `json:"status"`
	State            string  `json:"state"`
	Ports            string  `json:"ports"`
	Created          string  `json:"created"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryUsage      int64   `json:"memory_usage"`
	MemoryLimit      int64   `json:"memory_limit"`
	MemoryPercent    float64 `json:"memory_percent"`
	DiskReadBytes    int64   `json:"disk_read_bytes"`
	DiskWriteBytes   int64   `json:"disk_write_bytes"`
	BlockIOAvailable bool    `json:"block_io_available"`
	DiskUsage        int64   `json:"disk_usage"`
	DiskVirtualUsage int64   `json:"disk_virtual_usage"`
	DiskAvailable    bool    `json:"disk_available"`
	StatsAvailable   bool    `json:"stats_available"`
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

	// refresh=1 asks the host directly instead of trusting the cached row. This
	// is the recovery path for a server whose flag was cleared: without it the
	// only way back is to wait for a background poll to happen to succeed.
	if c.Query("refresh") == "1" {
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
		probe := services.ProbeDocker(client, server.ServerType)
		if probe.Known {
			// Same rule as the collector: an installed host whose daemon stayed
			// silent keeps its stored version rather than having it blanked.
			if probe.Installed && probe.Version == "" && installed {
				probe.Version = version
			}
			if probe.Installed != installed || probe.Version != version {
				if err := models.UpdateDockerInfo(db.Raw, id, probe.Installed, probe.Version); err != nil {
					c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save docker info"})
					return
				}
			}
			installed, version = probe.Installed, probe.Version
		}
		c.JSON(http.StatusOK, gin.H{
			"installed": installed,
			"version":   version,
			"refreshed": probe.Known,
		})
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

	// --size adds the writable-layer size to each row. It is supported by both
	// Linux and Windows Docker CLIs. The command is bounded because a host can
	// have an unexpectedly large number of containers.
	psFormat := `ps -a --size --format '{"id":"{{.ID}}","name":"{{.Names}}","image":"{{.Image}}","status":"{{.Status}}","state":"{{.State}}","ports":"{{.Ports}}","created":"{{.CreatedAt}}","size":"{{.Size}}"}'`
	output, _, err := services.RunDockerCmdBounded(client, psFormat, services.DockerCommandTimeout, services.DockerListOutputLimit)
	if err != nil {
		// Very old Docker versions may not understand --size. Retain the
		// container list in that case; metrics simply remain unavailable.
		output, _, err = services.RunDockerCmdBounded(client, `ps -a --format '{"id":"{{.ID}}","name":"{{.Names}}","image":"{{.Image}}","status":"{{.Status}}","state":"{{.State}}","ports":"{{.Ports}}","created":"{{.CreatedAt}}"}'`, services.DockerCommandTimeout, services.DockerListOutputLimit)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to list containers"})
		return
	}

	// `docker stats` only reports running containers. It is deliberately
	// best-effort: a stopped container still has useful ps metadata and disk
	// usage, even if a daemon refuses the stats request.
	statsOutput, _, _ := services.RunDockerCmdBounded(client, `stats --no-stream --format '{{json .}}'`, services.DockerCommandTimeout, services.DockerStatsOutputLimit)
	statsByID := services.ParseDockerStats(statsOutput)

	type dockerContainerWire struct {
		DockerContainer
		Size string `json:"size"`
	}
	var containers []DockerContainer
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var wire dockerContainerWire
		if err := json.Unmarshal([]byte(line), &wire); err == nil {
			dc := wire.DockerContainer
			dc.DiskUsage, dc.DiskVirtualUsage, dc.DiskAvailable = services.DockerSizesFromPS(wire.Size)
			if stat, ok := statsByID[dc.ID]; ok {
				dc.CPUPercent = stat.CPUPercent
				dc.MemoryUsage = stat.MemoryUsage
				dc.MemoryLimit = stat.MemoryLimit
				dc.MemoryPercent = stat.MemoryPercent
				dc.DiskReadBytes = stat.DiskReadBytes
				dc.DiskWriteBytes = stat.DiskWriteBytes
				dc.BlockIOAvailable = stat.BlockIOAvailable
				dc.StatsAvailable = stat.StatsAvailable
			} else {
				// Stats IDs are usually 12 characters, but some Docker versions
				// emit the full ID. Match either direction for compatibility.
				for statsID, stat := range statsByID {
					if strings.HasPrefix(statsID, dc.ID) || strings.HasPrefix(dc.ID, statsID) {
						dc.CPUPercent = stat.CPUPercent
						dc.MemoryUsage = stat.MemoryUsage
						dc.MemoryLimit = stat.MemoryLimit
						dc.MemoryPercent = stat.MemoryPercent
						dc.DiskReadBytes = stat.DiskReadBytes
						dc.DiskWriteBytes = stat.DiskWriteBytes
						dc.BlockIOAvailable = stat.BlockIOAvailable
						dc.StatsAvailable = stat.StatsAvailable
						break
					}
				}
			}
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
