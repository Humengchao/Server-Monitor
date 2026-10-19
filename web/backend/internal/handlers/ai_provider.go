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

type aiProviderClient interface {
	ValidateEndpoint(context.Context, string) error
	Test(context.Context, models.AIProvider) error
	ListModels(context.Context, models.AIProvider) ([]services.AIAvailableModel, error)
}
type AIProviderHandler struct {
	client aiProviderClient
	tests  chan struct{}
}

func NewAIProviderHandler(client *services.AIClient) *AIProviderHandler {
	return &AIProviderHandler{client: client, tests: make(chan struct{}, 4)}
}
func RegisterAIProviderRoutes(secured *gin.RouterGroup, client *services.AIClient) {
	h := NewAIProviderHandler(client)
	group := secured.Group("/ai/providers")
	group.GET("", h.List)
	group.POST("", h.Create)
	group.PUT("/:id", h.Update)
	group.DELETE("/:id", h.Delete)
	group.POST("/:id/test", middleware.RateLimit(6, time.Minute), h.Test)
	discoveryLimit := middleware.RateLimit(20, time.Minute)
	group.GET("/:id/models", discoveryLimit, h.Models)
	group.POST("/models", discoveryLimit, h.PreviewModels)
}

type aiProviderRequest struct {
	Name              *string    `json:"name"`
	Kind              *string    `json:"kind"`
	BaseURL           *string    `json:"base_url"`
	Model             *string    `json:"model"`
	APIKey            string     `json:"api_key"`
	Enabled           *bool      `json:"enabled"`
	ExpectedUpdatedAt *time.Time `json:"expected_updated_at"`
}

func readAIProviderRequest(c *gin.Context) (aiProviderRequest, bool) {
	var req aiProviderRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16384)
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid or oversized AI provider request"})
		return req, false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		c.JSON(400, gin.H{"error": "invalid AI provider request"})
		return req, false
	}
	return req, true
}
func aiProviderContext(c *gin.Context) (context.Context, context.CancelFunc, *models.DB, uuid.UUID) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	return ctx, cancel, c.MustGet("db").(*models.DB), c.MustGet("user_id").(uuid.UUID)
}
func aiProviderID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		c.JSON(400, gin.H{"error": "invalid AI provider ID"})
		return uuid.Nil, false
	}
	return id, true
}
func aiProviderStorageError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		c.JSON(404, gin.H{"error": "AI provider not found"})
	case errors.Is(err, models.ErrAIProviderLimit), errors.Is(err, models.ErrAIProviderConflict):
		c.JSON(409, gin.H{"error": err.Error()})
	case errors.Is(err, models.ErrAIProviderEndpointKey), errors.Is(err, models.ErrAIProviderKey), errors.Is(err, models.ErrAIProviderDisabled):
		c.JSON(400, gin.H{"error": err.Error()})
	default:
		c.JSON(500, gin.H{"error": "AI provider operation failed"})
	}
}
func (h *AIProviderHandler) List(c *gin.Context) {
	ctx, cancel, db, userID := aiProviderContext(c)
	defer cancel()
	providers, err := models.ListAIProviders(ctx, db, userID)
	if err != nil {
		aiProviderStorageError(c, err)
		return
	}
	result := make([]models.PublicAIProvider, 0, len(providers))
	for _, p := range providers {
		result = append(result, p.Public())
	}
	c.JSON(200, result)
}
func (h *AIProviderHandler) Create(c *gin.Context) { h.save(c, true) }
func (h *AIProviderHandler) Update(c *gin.Context) { h.save(c, false) }
func (h *AIProviderHandler) save(c *gin.Context, create bool) {
	var id uuid.UUID
	if !create {
		var ok bool
		id, ok = aiProviderID(c)
		if !ok {
			return
		}
	}
	req, ok := readAIProviderRequest(c)
	if !ok {
		return
	}
	ctx, cancel, db, userID := aiProviderContext(c)
	defer cancel()
	provider := models.AIProvider{UserID: userID, Kind: "deepseek", Enabled: true}
	oldURL := ""
	if !create {
		var err error
		provider, err = models.GetAIProvider(ctx, db, id, userID)
		if err != nil {
			aiProviderStorageError(c, err)
			return
		}
		if req.ExpectedUpdatedAt == nil || !req.ExpectedUpdatedAt.Equal(provider.UpdatedAt) {
			aiProviderStorageError(c, models.ErrAIProviderConflict)
			return
		}
		oldURL = provider.BaseURL
		// A decrypted old key must never count as newly supplied input when
		// an endpoint is changed. Save also enforces this inside its lock.
		provider.APIKey = ""
	}
	if req.Name != nil {
		provider.Name = *req.Name
	}
	if req.Kind != nil {
		provider.Kind = *req.Kind
	}
	if req.BaseURL != nil {
		provider.BaseURL = *req.BaseURL
	}
	if req.Model != nil {
		provider.Model = *req.Model
	}
	if req.Enabled != nil {
		provider.Enabled = *req.Enabled
	}
	provider.APIKey = req.APIKey
	if err := models.NormalizeAIProvider(&provider); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if !create && provider.BaseURL != oldURL && req.APIKey == "" {
		aiProviderStorageError(c, models.ErrAIProviderEndpointKey)
		return
	}
	if create && req.APIKey == "" {
		aiProviderStorageError(c, models.ErrAIProviderKey)
		return
	}
	// No credentials are sent here: resolve only. Every actual request repeats
	// resolution and pins its approved IPs, including after a DNS change.
	if create || provider.BaseURL != oldURL || req.Enabled != nil && *req.Enabled {
		if err := h.client.ValidateEndpoint(ctx, provider.BaseURL); err != nil {
			code, message := services.SafeAIProviderError(err)
			c.JSON(400, gin.H{"error": message, "code": code})
			return
		}
	}
	if err := models.SaveAIProvider(ctx, db, &provider, create); err != nil {
		aiProviderStorageError(c, err)
		return
	}
	status := http.StatusOK
	if create {
		status = http.StatusCreated
	}
	c.JSON(status, provider.Public())
}
func (h *AIProviderHandler) Delete(c *gin.Context) {
	id, ok := aiProviderID(c)
	if !ok {
		return
	}
	ctx, cancel, db, userID := aiProviderContext(c)
	defer cancel()
	if err := models.DeleteAIProvider(ctx, db, id, userID); err != nil {
		aiProviderStorageError(c, err)
		return
	}
	c.Status(204)
}
func (h *AIProviderHandler) Test(c *gin.Context) {
	id, ok := aiProviderID(c)
	if !ok {
		return
	}
	ctx, cancel, db, userID := aiProviderContext(c)
	provider, err := models.GetAIProvider(ctx, db, id, userID)
	cancel()
	if err != nil {
		aiProviderStorageError(c, err)
		return
	}
	if !provider.Enabled {
		aiProviderStorageError(c, models.ErrAIProviderDisabled)
		return
	}
	select {
	case h.tests <- struct{}{}:
		defer func() { <-h.tests }()
	default:
		c.JSON(429, gin.H{"error": "AI provider tests are busy"})
		return
	}
	if err = h.client.Test(c.Request.Context(), provider); err != nil {
		code, message := services.SafeAIProviderError(err)
		// Upstream authentication errors are not login failures for this app.
		status := http.StatusBadGateway
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		c.JSON(status, gin.H{"ok": false, "error": message, "code": code})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "model": provider.Model})
}

// Models never accepts a replacement URL or key: it can only contact the
// authenticated owner's saved endpoint using its encrypted-at-rest key.
func (h *AIProviderHandler) Models(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, ok := aiProviderID(c)
	if !ok {
		return
	}
	ctx, cancel, db, userID := aiProviderContext(c)
	provider, err := models.GetAIProvider(ctx, db, id, userID)
	cancel()
	if err != nil {
		aiProviderStorageError(c, err)
		return
	}
	if !provider.Enabled {
		aiProviderStorageError(c, models.ErrAIProviderDisabled)
		return
	}
	h.discoverModels(c, provider)
}

// PreviewModels resolves the initial-setup dependency: the user can discover
// model IDs before choosing and saving one. Only an explicitly supplied key
// is accepted, never a provider ID or a key recovered from another endpoint.
func (h *AIProviderHandler) PreviewModels(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	req, ok := readAIProviderRequest(c)
	if !ok {
		return
	}
	provider := models.AIProvider{Name: "Model discovery", Kind: "compatible", Model: "model-catalog", APIKey: req.APIKey, Enabled: true}
	if req.Kind != nil {
		provider.Kind = *req.Kind
	}
	if req.BaseURL != nil {
		provider.BaseURL = *req.BaseURL
	}
	if err := models.NormalizeAIProvider(&provider); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if provider.APIKey == "" {
		aiProviderStorageError(c, models.ErrAIProviderKey)
		return
	}
	h.discoverModels(c, provider)
}

func (h *AIProviderHandler) discoverModels(c *gin.Context, provider models.AIProvider) {
	select {
	case h.tests <- struct{}{}:
		defer func() { <-h.tests }()
	default:
		c.JSON(429, gin.H{"error": "AI provider requests are busy"})
		return
	}
	available, err := h.client.ListModels(c.Request.Context(), provider)
	if err != nil {
		code, message := services.SafeAIProviderError(err)
		status := http.StatusBadGateway
		if code == "timeout" {
			status = http.StatusGatewayTimeout
		}
		c.JSON(status, gin.H{"error": message, "code": code})
		return
	}
	if available == nil {
		available = []services.AIAvailableModel{}
	}
	c.JSON(http.StatusOK, gin.H{"models": available, "fetched_at": time.Now().UTC(), "source": "provider"})
}
