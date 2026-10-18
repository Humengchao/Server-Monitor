package handlers

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	"server-monitor/internal/services"
	"strconv"
	"strings"
	"time"
)

type LogsHandler struct {
	cache *services.SSHConnCache
	slots chan struct{}
}

func NewLogsHandler(cache *services.SSHConnCache) *LogsHandler {
	return &LogsHandler{cache: cache, slots: make(chan struct{}, 4)}
}
func parseLogsQuery(c *gin.Context) (services.LogsQuery, error) {
	q := services.LogsQuery{Since: time.Now().Add(-time.Hour), Unit: c.Query("unit"), Priority: c.Query("priority"), Keyword: c.Query("keyword"), Cursor: c.Query("cursor"), Limit: 200}
	var err error
	if q.Cursor != "" && c.Query("since") == "" {
		return q, fmt.Errorf("since is required when continuing a cursor")
	}
	if text := c.Query("since"); text != "" {
		q.Since, err = time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return q, fmt.Errorf("invalid since")
		}
	}
	if text := c.Query("until"); text != "" {
		q.Until, err = time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return q, fmt.Errorf("invalid until")
		}
	}
	if text := c.Query("limit"); text != "" {
		q.Limit, err = strconv.Atoi(text)
		if err != nil {
			return q, fmt.Errorf("invalid limit")
		}
	}
	end := q.Until
	if end.IsZero() {
		end = time.Now()
	}
	if q.Limit < 1 || q.Limit > 500 || len(q.Keyword) > 512 || len(q.Unit) > 256 || len(q.Cursor) > 8192 || strings.ContainsRune(q.Keyword, 0) || q.Since.After(end) || end.Sub(q.Since) > 7*24*time.Hour {
		return q, fmt.Errorf("invalid log query; maximum range seven days, limit 500")
	}
	return q, nil
}
func (h *LogsHandler) Journal(c *gin.Context) { h.read(c, false) }
func (h *LogsHandler) Docker(c *gin.Context)  { h.read(c, true) }
func (h *LogsHandler) read(c *gin.Context, docker bool) {
	q, err := parseLogsQuery(c)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	container := c.Param("containerId")
	if docker && !validDockerID.MatchString(container) {
		c.JSON(400, gin.H{"error": "invalid container"})
		return
	}
	if docker {
		_, _, err = services.DockerLogsCommand(q, container)
	} else {
		_, _, err = services.JournalCommand(q)
	}
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	server, ok := resolveOwnedServer(c)
	if !ok {
		return
	}
	if !docker && server.ServerType == "windows" {
		c.JSON(200, services.LogsPage{Entries: []services.LogEntry{}, Partial: true, Reason: "unsupported"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), services.LogsTimeout)
	defer cancel()
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	case <-ctx.Done():
		c.JSON(503, gin.H{"error": "log readers busy"})
		return
	}
	client, err := h.cache.GetContext(ctx, server)
	if err != nil {
		c.JSON(502, gin.H{"error": "source connection failed"})
		return
	}
	run := func(ctx context.Context, command string) (services.LogsCommandResult, error) {
		return services.RunLogCommand(ctx, client, command)
	}
	var page services.LogsPage
	if docker {
		page, err = services.ReadDockerLogs(ctx, q, container, run)
	} else {
		page, err = services.ReadJournal(ctx, q, run)
	}
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	c.JSON(200, page)
}
