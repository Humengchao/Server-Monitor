package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/middleware"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
)

type aiExtensionClient interface {
	ValidateEndpoint(context.Context, string) error
	ListTools(context.Context, models.AIPlugin) ([]services.AIMCPTool, error)
}
type AIExtensionHandler struct {
	client   aiExtensionClient
	requests chan struct{}
}

func NewAIExtensionHandler(client *services.AIMCPClient) *AIExtensionHandler {
	return &AIExtensionHandler{client: client, requests: make(chan struct{}, 4)}
}
func RegisterAIExtensionRoutes(secured *gin.RouterGroup, client *services.AIMCPClient) {
	h := NewAIExtensionHandler(client)
	skills := secured.Group("/ai/skills")
	skills.GET("", h.ListSkills)
	skills.GET("/:id", h.GetSkill)
	skills.POST("", h.CreateSkill)
	skills.PUT("/:id", h.UpdateSkill)
	skills.DELETE("/:id", h.DeleteSkill)
	plugins := secured.Group("/ai/plugins")
	plugins.GET("", h.ListPlugins)
	plugins.POST("", h.CreatePlugin)
	plugins.PUT("/:id", h.UpdatePlugin)
	plugins.DELETE("/:id", h.DeletePlugin)
	plugins.POST("/:id/test", middleware.RateLimit(6, time.Minute), h.TestPlugin)
	plugins.GET("/:id/tools", middleware.RateLimit(12, time.Minute), h.PluginTools)
}
func aiExtensionBody(c *gin.Context, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		c.JSON(400, gin.H{"error": "invalid extension request"})
		return false
	}
	if _, err := decoder.Token(); err != io.EOF {
		c.JSON(400, gin.H{"error": "invalid extension request"})
		return false
	}
	return true
}
func aiExtensionID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		c.JSON(400, gin.H{"error": "invalid extension ID"})
		return uuid.Nil, false
	}
	return id, true
}
func aiExtensionStorageError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		c.JSON(404, gin.H{"error": "extension not found"})
	case errors.Is(err, models.ErrAIExtensionConflict), errors.Is(err, models.ErrAIExtensionLimit):
		c.JSON(409, gin.H{"error": err.Error()})
	case errors.Is(err, models.ErrAIPluginEndpointToken), errors.Is(err, models.ErrAIPluginDisabled):
		c.JSON(400, gin.H{"error": err.Error()})
	default:
		c.JSON(500, gin.H{"error": "extension operation failed"})
	}
}
func (h *AIExtensionHandler) ListSkills(c *gin.Context) {
	ctx, cancel, db, user := aiProviderContext(c)
	defer cancel()
	skills, err := models.ListAISkills(ctx, db, user)
	if err != nil {
		aiExtensionStorageError(c, err)
		return
	}
	c.JSON(200, skills)
}
func (h *AIExtensionHandler) GetSkill(c *gin.Context) {
	id, ok := aiExtensionID(c)
	if !ok {
		return
	}
	ctx, cancel, db, user := aiProviderContext(c)
	defer cancel()
	skill, err := models.GetAISkill(ctx, db, id, user)
	if err != nil {
		aiExtensionStorageError(c, err)
		return
	}
	c.JSON(200, skill)
}

type aiSkillRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Content     *string `json:"content"`
}

func (h *AIExtensionHandler) CreateSkill(c *gin.Context) { h.saveSkill(c, true) }
func (h *AIExtensionHandler) UpdateSkill(c *gin.Context) { h.saveSkill(c, false) }
func (h *AIExtensionHandler) saveSkill(c *gin.Context, create bool) {
	var req aiSkillRequest
	if !aiExtensionBody(c, &req) {
		return
	}
	ctx, cancel, db, user := aiProviderContext(c)
	defer cancel()
	s := models.AISkill{UserID: user}
	if !create {
		id, ok := aiExtensionID(c)
		if !ok {
			return
		}
		var err error
		s, err = models.GetAISkill(ctx, db, id, user)
		if err != nil {
			aiExtensionStorageError(c, err)
			return
		}
	}
	if req.Name != nil {
		s.Name = *req.Name
	}
	if req.Description != nil {
		s.Description = *req.Description
	}
	if req.Content != nil {
		s.Content = *req.Content
	}
	if err := models.NormalizeAISkill(&s); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := models.SaveAISkill(ctx, db, &s, create); err != nil {
		aiExtensionStorageError(c, err)
		return
	}
	status := 200
	if create {
		status = 201
	}
	c.JSON(status, s)
}
func (h *AIExtensionHandler) DeleteSkill(c *gin.Context) {
	id, ok := aiExtensionID(c)
	if !ok {
		return
	}
	ctx, cancel, db, user := aiProviderContext(c)
	defer cancel()
	if err := models.DeleteAISkill(ctx, db, id, user); err != nil {
		aiExtensionStorageError(c, err)
		return
	}
	c.Status(204)
}
func (h *AIExtensionHandler) ListPlugins(c *gin.Context) {
	ctx, cancel, db, user := aiProviderContext(c)
	defer cancel()
	plugins, err := models.ListAIPlugins(ctx, db, user)
	if err != nil {
		aiExtensionStorageError(c, err)
		return
	}
	result := make([]models.PublicAIPlugin, 0, len(plugins))
	for _, p := range plugins {
		result = append(result, p.Public())
	}
	c.JSON(200, result)
}

type aiPluginRequest struct {
	Name        *string `json:"name"`
	URL         *string `json:"url"`
	BearerToken string  `json:"bearer_token"`
	ClearToken  bool    `json:"clear_token"`
	Enabled     *bool   `json:"enabled"`
}

func (h *AIExtensionHandler) CreatePlugin(c *gin.Context) { h.savePlugin(c, true) }
func (h *AIExtensionHandler) UpdatePlugin(c *gin.Context) { h.savePlugin(c, false) }
func (h *AIExtensionHandler) savePlugin(c *gin.Context, create bool) {
	var req aiPluginRequest
	if !aiExtensionBody(c, &req) {
		return
	}
	if req.ClearToken && req.BearerToken != "" {
		c.JSON(400, gin.H{"error": "cannot set and clear a bearer token together"})
		return
	}
	ctx, cancel, db, user := aiProviderContext(c)
	defer cancel()
	p := models.AIPlugin{UserID: user, Enabled: true}
	oldURL := ""
	hadToken := false
	if !create {
		id, ok := aiExtensionID(c)
		if !ok {
			return
		}
		var err error
		p, err = models.GetAIPlugin(ctx, db, id, user)
		if err != nil {
			aiExtensionStorageError(c, err)
			return
		}
		oldURL = p.URL
		hadToken = p.BearerToken != ""
	}
	p.BearerToken = req.BearerToken
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.URL != nil {
		p.URL = *req.URL
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if err := models.NormalizeAIPlugin(&p); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if oldURL != "" && oldURL != p.URL && hadToken && req.BearerToken == "" && !req.ClearToken {
		aiExtensionStorageError(c, models.ErrAIPluginEndpointToken)
		return
	}
	if create || oldURL != p.URL || req.Enabled != nil && *req.Enabled {
		if err := h.client.ValidateEndpoint(ctx, p.URL); err != nil {
			code, message := services.SafeAIMCPError(err)
			c.JSON(400, gin.H{"code": code, "error": message})
			return
		}
	}
	if err := models.SaveAIPlugin(ctx, db, &p, create, req.ClearToken); err != nil {
		aiExtensionStorageError(c, err)
		return
	}
	status := 200
	if create {
		status = 201
	}
	c.JSON(status, p.Public())
}
func (h *AIExtensionHandler) DeletePlugin(c *gin.Context) {
	id, ok := aiExtensionID(c)
	if !ok {
		return
	}
	ctx, cancel, db, user := aiProviderContext(c)
	defer cancel()
	if err := models.DeleteAIPlugin(ctx, db, id, user); err != nil {
		aiExtensionStorageError(c, err)
		return
	}
	c.Status(204)
}
func (h *AIExtensionHandler) TestPlugin(c *gin.Context)  { h.fetchTools(c, true) }
func (h *AIExtensionHandler) PluginTools(c *gin.Context) { h.fetchTools(c, false) }
func (h *AIExtensionHandler) fetchTools(c *gin.Context, test bool) {
	id, ok := aiExtensionID(c)
	if !ok {
		return
	}
	ctx, cancel, db, user := aiProviderContext(c)
	p, err := models.GetAIPlugin(ctx, db, id, user)
	cancel()
	if err != nil {
		aiExtensionStorageError(c, err)
		return
	}
	if !p.Enabled {
		aiExtensionStorageError(c, models.ErrAIPluginDisabled)
		return
	}
	select {
	case h.requests <- struct{}{}:
		defer func() { <-h.requests }()
	default:
		c.JSON(429, gin.H{"error": "MCP requests are busy"})
		return
	}
	tools, err := h.client.ListTools(c.Request.Context(), p)
	if err != nil {
		code, message := services.SafeAIMCPError(err)
		status := 502
		if errors.Is(err, context.DeadlineExceeded) {
			status = 504
		}
		c.JSON(status, gin.H{"ok": false, "code": code, "error": message})
		return
	}
	if test {
		c.JSON(200, gin.H{"ok": true, "tool_count": len(tools)})
		return
	}
	c.JSON(200, gin.H{"tools": tools, "fetched_at": time.Now().UTC()})
}
