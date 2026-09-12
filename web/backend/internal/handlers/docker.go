package handlers

import (
	"context"
	"crypto/sha256"
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
	sshCache   *services.SSHConnCache
	results    *services.RequestCache[[]DockerContainer]
	statistics *services.RequestCache[[]DockerContainer]
}

func NewDockerHandler(sshCache *services.SSHConnCache) *DockerHandler {
	return &DockerHandler{sshCache: sshCache, results: services.NewRequestCache[[]DockerContainer](4), statistics: services.NewRequestCache[[]DockerContainer](4)}
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

func dockerCacheKey(server *models.Server, detailed bool) string {
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s:%s:%s:%s", server.Host, server.Port, server.SSHUsername, server.SSHPassword, server.SSHKey, server.SSHHostKey)))
	return fmt.Sprintf("%s:%x:%t", server.ID, fingerprint, detailed)
}
func (h *DockerHandler) ListContainers(c *gin.Context) { h.containers(c, false) }
func (h *DockerHandler) ContainerStats(c *gin.Context) { h.containers(c, true) }
func (h *DockerHandler) containers(c *gin.Context, detailed bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid id"})
		return
	}
	server, err := models.GetServerByIDAndUser(c.MustGet("db").(*models.DB), id, c.MustGet("user_id").(uuid.UUID))
	if err != nil {
		c.JSON(404, gin.H{"error": "server not found"})
		return
	}
	ttl := 3 * time.Second
	cache := h.results
	if detailed {
		ttl = 10 * time.Second
		cache = h.statistics
	}
	result, err := cache.Get(c.Request.Context(), dockerCacheKey(server, detailed), ttl, func(ctx context.Context) ([]DockerContainer, error) { return h.loadContainers(ctx, server, detailed) })
	if err != nil {
		c.JSON(502, gin.H{"error": "failed to read Docker data"})
		return
	}
	c.JSON(200, result)
}
func (h *DockerHandler) loadContainers(ctx context.Context, server *models.Server, detailed bool) ([]DockerContainer, error) {
	client, err := h.sshCache.Get(server)
	if err != nil {
		return nil, err
	}
	return readDockerContainers(ctx, detailed, func(task context.Context, arguments string, limit int) (string, error) {
		return services.RunDockerContext(task, client, arguments, limit)
	})
}

func readDockerContainers(ctx context.Context, detailed bool, run func(context.Context, string, int) (string, error)) ([]DockerContainer, error) {
	options := "ps -a --no-trunc "
	if detailed {
		options += "--size "
	}
	format := `--format '{"id":"{{.ID}}","name":"{{.Names}}","image":"{{.Image}}","status":"{{.Status}}","state":"{{.State}}","ports":"{{.Ports}}","created":"{{.CreatedAt}}","size":"{{.Size}}"}'`
	output, err := run(ctx, options+format, services.DockerListOutputLimit)
	if err != nil && detailed && ctx.Err() == nil {
		output, err = run(ctx, "ps -a --no-trunc "+format, services.DockerListOutputLimit)
	}
	if err != nil {
		return nil, err
	}
	statsByID := map[string]services.DockerContainerStats{}
	if detailed {
		statsOutput, statsErr := run(ctx, `stats --no-stream --no-trunc --format '{{json .}}'`, services.DockerStatsOutputLimit)
		if statsErr != nil {
			return nil, statsErr
		}
		statsByID = services.ParseDockerStats(statsOutput)
	}
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
			if !detailed {
				dc.DiskAvailable = false
			}
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

	return containers, nil
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

	defer h.results.Forget(dockerCacheKey(server, false))
	defer h.statistics.Forget(dockerCacheKey(server, true))
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
