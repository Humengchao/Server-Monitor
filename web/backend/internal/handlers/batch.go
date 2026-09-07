package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// BatchHandler serves the dashboard's multi-select actions. Every operation is
// scoped to the caller's own servers at the SQL level.
type BatchHandler struct {
	sshCache *services.SSHConnCache
}

func NewBatchHandler(sshCache *services.SSHConnCache) *BatchHandler {
	return &BatchHandler{sshCache: sshCache}
}

type BulkTagRequest struct {
	ServerIDs []uuid.UUID `json:"server_ids" binding:"required"`
	TagIDs    []uuid.UUID `json:"tag_ids" binding:"required"`
	// Action is "add" or "remove".
	Action string `json:"action" binding:"required"`
}

type BulkDeleteRequest struct {
	ServerIDs []uuid.UUID `json:"server_ids" binding:"required"`
}

type BulkExecRequest struct {
	ServerIDs []uuid.UUID `json:"server_ids" binding:"required"`
	Command   string      `json:"command" binding:"required"`
}

// dedupeIDs removes duplicates while keeping the caller's ordering, and rejects
// an oversized selection.
func dedupeIDs(ids []uuid.UUID, limit int) ([]uuid.UUID, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("no servers selected")
	}
	seen := make(map[uuid.UUID]struct{}, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if limit > 0 && len(unique) > limit {
		return nil, fmt.Errorf("select at most %d servers at a time", limit)
	}
	return unique, nil
}

func (h *BatchHandler) BulkTags(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req BulkTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action != "add" && action != "remove" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "action must be add or remove"})
		return
	}
	serverIDs, err := dedupeIDs(req.ServerIDs, 0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.TagIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no tags selected"})
		return
	}

	db := c.MustGet("db").(*models.DB)
	// Count first: the statements below filter by owner, so without this the
	// response would claim to have updated servers that belong to someone else.
	owned, err := models.CountOwnedServers(db.Raw, serverIDs, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify server ownership"})
		return
	}
	if owned == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no matching servers"})
		return
	}
	if action == "add" {
		err = models.AddTagsToServers(db.Raw, serverIDs, req.TagIDs, userID)
	} else {
		err = models.RemoveTagsFromServers(db.Raw, serverIDs, req.TagIDs, userID)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update tags"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "tags updated", "servers": owned})
}

func (h *BatchHandler) BulkDelete(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req BulkDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	serverIDs, err := dedupeIDs(req.ServerIDs, 0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	db := c.MustGet("db").(*models.DB)
	deleted, err := models.DeleteServers(db.Raw, serverIDs, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete servers"})
		return
	}
	// Drop any cached SSH connection so nothing stays connected to a host that
	// is no longer in the panel.
	for _, id := range serverIDs {
		h.sshCache.Drop(id)
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted", "deleted": deleted})
}

// normalizeBatchCommand rejects input that cannot be a single shell command
// line. Control characters other than tab would let one "command" smuggle in
// extra lines that the confirmation dialog never showed the user.
func normalizeBatchCommand(command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("command is required")
	}
	if len(command) > services.BatchMaxCommandLength {
		return "", fmt.Errorf("command must be at most %d characters", services.BatchMaxCommandLength)
	}
	if strings.ContainsFunc(command, func(r rune) bool {
		return r != '\t' && unicode.IsControl(r)
	}) {
		return "", fmt.Errorf("command must be a single line without control characters")
	}
	return command, nil
}

func (h *BatchHandler) BulkExec(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req BulkExecRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	command, err := normalizeBatchCommand(req.Command)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	serverIDs, err := dedupeIDs(req.ServerIDs, services.BatchMaxTargets)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	db := c.MustGet("db").(*models.DB)
	servers, err := models.GetServersByIDsAndUser(db, serverIDs, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load servers"})
		return
	}
	if len(servers) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "no matching servers"})
		return
	}

	results := services.RunBatchCommand(h.sshCache, servers, command)
	succeeded := 0
	for _, r := range results {
		if r.OK {
			succeeded++
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"results":   results,
		"succeeded": succeeded,
		"failed":    len(results) - succeeded,
	})
}
