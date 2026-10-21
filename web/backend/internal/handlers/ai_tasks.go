package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/middleware"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
)

type AITaskHandler struct{ worker *services.AITaskWorker }

func RegisterAITaskRoutes(group *gin.RouterGroup, worker *services.AITaskWorker) {
	h := &AITaskHandler{worker: worker}
	tasks := group.Group("/ai/tasks")
	tasks.GET("", h.List)
	tasks.POST("", middleware.RateLimit(6, time.Minute), h.Create)
	tasks.GET("/:id", h.Get)
	tasks.POST("/:id/cancel", h.Cancel)
	tasks.POST("/:id/messages", middleware.RateLimit(12, time.Minute), h.Message)
	tasks.POST("/:id/actions/:action_id/decision", middleware.RateLimit(30, time.Minute), h.Decide)
}
func aiContext(c *gin.Context) (context.Context, context.CancelFunc, *models.DB, uuid.UUID) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	return ctx, cancel, c.MustGet("db").(*models.DB), c.MustGet("user_id").(uuid.UUID)
}
func aiTaskID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid task ID"})
		return id, false
	}
	return id, true
}
func aiTaskError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		c.JSON(404, gin.H{"error": "task, server or provider not found"})
	case errors.Is(err, models.ErrAITaskConflict), errors.Is(err, models.ErrAITaskLimit), errors.Is(err, models.ErrAIRequestConflict), errors.Is(err, models.ErrAITaskStepLimit), errors.Is(err, models.ErrAIConversationLimit):
		c.JSON(409, gin.H{"error": err.Error()})
	case errors.Is(err, models.ErrAIProviderDisabled), errors.Is(err, models.ErrAIPluginDisabled), errors.Is(err, models.ErrAITaskOptions), errors.Is(err, models.ErrAITaskScope), errors.Is(err, services.ErrAISecretInput), errors.Is(err, services.ErrAIAgentNotEnrolled):
		c.JSON(400, gin.H{"error": err.Error()})
	default:
		c.JSON(500, gin.H{"error": "AI task operation failed; refresh its status before retrying"})
	}
}
func readAITaskRequest(c *gin.Context, request any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 40*1024)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(request); err != nil {
		c.JSON(400, gin.H{"error": "invalid AI task request"})
		return false
	}
	if d.Decode(&struct{}{}) != io.EOF {
		c.JSON(400, gin.H{"error": "invalid AI task request"})
		return false
	}
	return true
}
func validAIPrompt(message string) bool {
	return strings.TrimSpace(message) != "" && len(message) <= 16000 && utf8.ValidString(message) && !strings.ContainsRune(message, 0)
}

func (h *AITaskHandler) Create(c *gin.Context) {
	var req struct {
		Scope        string      `json:"scope"`
		ServerID     uuid.UUID   `json:"server_id"`
		ProviderID   uuid.UUID   `json:"provider_id"`
		RequestID    uuid.UUID   `json:"request_id"`
		Prompt       string      `json:"prompt"`
		ApprovalMode string      `json:"approval_mode"`
		SkillIDs     []uuid.UUID `json:"skill_ids"`
		PluginIDs    []uuid.UUID `json:"plugin_ids"`
	}
	if !readAITaskRequest(c, &req) {
		return
	}
	selection := models.AITaskSelection{Scope: req.Scope, ApprovalMode: req.ApprovalMode, SkillIDs: req.SkillIDs, PluginIDs: req.PluginIDs}
	if err := selection.Normalize(); err != nil {
		aiTaskError(c, err)
		return
	}
	if (selection.Scope == "server" && req.ServerID == uuid.Nil) || (selection.Scope == "fleet" && req.ServerID != uuid.Nil) {
		c.JSON(400, gin.H{"error": models.ErrAITaskScope.Error()})
		return
	}
	if req.ProviderID == uuid.Nil || req.RequestID == uuid.Nil || !validAIPrompt(req.Prompt) {
		c.JSON(400, gin.H{"error": "select a server and provider, supply a unique request ID, and enter a task of 1 to 16000 bytes"})
		return
	}
	ctx, cancel, _, user := aiContext(c)
	defer cancel()
	task, err := h.worker.Create(ctx, user, req.ServerID, req.ProviderID, req.RequestID, strings.TrimSpace(req.Prompt), selection)
	if err != nil {
		aiTaskError(c, err)
		return
	}
	c.JSON(202, task)
}
func (h *AITaskHandler) List(c *gin.Context) {
	limit := 30
	if raw := c.Query("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 50 {
			c.JSON(400, gin.H{"error": "limit must be between 1 and 50"})
			return
		}
		limit = value
	}
	before := c.Query("before")
	if len(before) > 256 {
		c.JSON(400, gin.H{"error": "invalid task cursor"})
		return
	}
	ctx, cancel, db, user := aiContext(c)
	defer cancel()
	tasks, next, err := models.ListAITasks(ctx, db.Raw, user, limit, before)
	if err != nil {
		aiTaskError(c, err)
		return
	}
	c.JSON(200, gin.H{"tasks": tasks, "next_cursor": next})
}
func (h *AITaskHandler) Get(c *gin.Context) {
	id, ok := aiTaskID(c)
	if !ok {
		return
	}
	ctx, cancel, db, user := aiContext(c)
	defer cancel()
	task, events, actions, err := models.AITaskDetails(ctx, db.Raw, id, user)
	if err != nil {
		aiTaskError(c, err)
		return
	}
	c.JSON(200, gin.H{"task": task, "events": events, "actions": actions})
}
func (h *AITaskHandler) Cancel(c *gin.Context) {
	id, ok := aiTaskID(c)
	if !ok {
		return
	}
	ctx, cancel, db, user := aiContext(c)
	defer cancel()
	task, err := models.CancelAITask(ctx, db.Raw, id, user)
	if err != nil {
		aiTaskError(c, err)
		return
	}
	c.JSON(200, task)
}
func (h *AITaskHandler) Decide(c *gin.Context) {
	id, ok := aiTaskID(c)
	if !ok {
		return
	}
	action, err := uuid.Parse(c.Param("action_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid operation ID"})
		return
	}
	var req struct {
		Decision string `json:"decision"`
	}
	if !readAITaskRequest(c, &req) {
		return
	}
	if req.Decision != "approve" && req.Decision != "reject" {
		c.JSON(400, gin.H{"error": "decision must be approve or reject"})
		return
	}
	ctx, cancel, db, user := aiContext(c)
	defer cancel()
	task, err := models.DecideAIAction(ctx, db.Raw, id, action, user, req.Decision == "approve")
	if err != nil {
		aiTaskError(c, err)
		return
	}
	h.worker.Wake()
	c.JSON(200, task)
}
func (h *AITaskHandler) Message(c *gin.Context) {
	id, ok := aiTaskID(c)
	if !ok {
		return
	}
	var req struct {
		RequestID uuid.UUID `json:"request_id"`
		Message   string    `json:"message"`
	}
	if !readAITaskRequest(c, &req) {
		return
	}
	if req.RequestID == uuid.Nil || !validAIPrompt(req.Message) {
		c.JSON(400, gin.H{"error": "a unique request ID and a message of 1 to 16000 bytes are required"})
		return
	}
	ctx, cancel, _, user := aiContext(c)
	defer cancel()
	task, err := h.worker.Continue(ctx, user, id, req.RequestID, strings.TrimSpace(req.Message))
	if err != nil {
		aiTaskError(c, err)
		return
	}
	c.JSON(202, task)
}
