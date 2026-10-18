package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/middleware"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
)

type NotificationHandler struct{}

func NewNotificationHandler() *NotificationHandler { return &NotificationHandler{} }

// The caller supplies an /api group with authentication and DB middleware.
func RegisterNotificationRoutes(authenticated *gin.RouterGroup) {
	h := NewNotificationHandler()
	group := authenticated.Group("/notifications")
	group.GET("/channels", h.ListChannels)
	group.POST("/channels", h.CreateChannel)
	group.PUT("/channels/:id", h.UpdateChannel)
	group.DELETE("/channels/:id", h.DeleteChannel)
	group.POST("/channels/:id/test", middleware.RateLimit(6, time.Minute), h.TestChannel)
	group.GET("/deliveries", h.ListDeliveries)
	group.POST("/deliveries/:id/retry", middleware.RateLimit(12, time.Minute), h.RetryDelivery)
}

type notificationChannelRequest struct {
	Name              *string                    `json:"name"`
	Type              string                     `json:"type"`
	Enabled           *bool                      `json:"enabled"`
	Config            map[string]json.RawMessage `json:"config"`
	ClearSecretFields []string                   `json:"clear_secret_fields"`
}

func notificationContext(c *gin.Context) (context.Context, context.CancelFunc, *models.DB, uuid.UUID) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	return ctx, cancel, c.MustGet("db").(*models.DB), c.MustGet("user_id").(uuid.UUID)
}
func notificationUUID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid channel ID"})
		return uuid.Nil, false
	}
	return id, true
}
func notificationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		c.JSON(404, gin.H{"error": "notification channel or delivery not found"})
	case errors.Is(err, models.ErrNotificationConflict):
		c.JSON(409, gin.H{"error": err.Error()})
	case errors.Is(err, models.ErrNotificationDisabled), errors.Is(err, models.ErrNotificationCursor):
		c.JSON(400, gin.H{"error": err.Error()})
	default:
		c.JSON(500, gin.H{"error": "notification operation failed"})
	}
}
func readNotificationRequest(c *gin.Context, request any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32768)
	if err := c.ShouldBindJSON(request); err != nil {
		c.JSON(400, gin.H{"error": "invalid notification request"})
		return false
	}
	return true
}
func (h *NotificationHandler) ListChannels(c *gin.Context) {
	ctx, cancel, db, user := notificationContext(c)
	defer cancel()
	channels, err := models.ListNotificationChannels(ctx, db, user)
	if err != nil {
		notificationError(c, err)
		return
	}
	result := make([]models.PublicNotificationChannel, 0, len(channels))
	for _, channel := range channels {
		result = append(result, channel.Public())
	}
	c.JSON(200, result)
}
func (h *NotificationHandler) CreateChannel(c *gin.Context) {
	var req notificationChannelRequest
	if !readNotificationRequest(c, &req) {
		return
	}
	name := ""
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	if name == "" || len(name) > 128 {
		c.JSON(400, gin.H{"error": "channel name must be 1 to 128 characters"})
		return
	}
	config, err := services.MergeNotificationConfig(req.Type, models.NotificationConfig{}, req.Config, req.ClearSecretFields)
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	ctx, cancel, db, user := notificationContext(c)
	defer cancel()
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	channel := models.NotificationChannel{UserID: user, Name: name, Type: req.Type, Enabled: enabled, Config: config}
	if err = models.CreateNotificationChannel(ctx, db, &channel); err != nil {
		notificationError(c, err)
		return
	}
	c.JSON(201, channel.Public())
}
func (h *NotificationHandler) UpdateChannel(c *gin.Context) {
	id, ok := notificationUUID(c)
	if !ok {
		return
	}
	var req notificationChannelRequest
	if !readNotificationRequest(c, &req) {
		return
	}
	ctx, cancel, db, user := notificationContext(c)
	defer cancel()
	channel, err := models.GetNotificationChannel(ctx, db, id, user)
	if err != nil {
		notificationError(c, err)
		return
	}
	if req.Type != "" && req.Type != channel.Type {
		c.JSON(400, gin.H{"error": "channel type cannot be changed"})
		return
	}
	if req.Name != nil {
		channel.Name = strings.TrimSpace(*req.Name)
	}
	if channel.Name == "" || len(channel.Name) > 128 {
		c.JSON(400, gin.H{"error": "channel name must be 1 to 128 characters"})
		return
	}
	if req.Enabled != nil {
		channel.Enabled = *req.Enabled
	}
	// Disabling is always possible, including an imported channel whose
	// omitted secrets have not yet been filled in.
	if channel.Enabled || len(req.Config) > 0 || len(req.ClearSecretFields) > 0 {
		channel.Config, err = services.MergeNotificationConfig(channel.Type, channel.Config, req.Config, req.ClearSecretFields)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
	}
	if err = models.UpdateNotificationChannel(ctx, db, &channel); err != nil {
		notificationError(c, err)
		return
	}
	c.JSON(200, channel.Public())
}
func (h *NotificationHandler) DeleteChannel(c *gin.Context) {
	id, ok := notificationUUID(c)
	if !ok {
		return
	}
	ctx, cancel, db, user := notificationContext(c)
	defer cancel()
	if err := models.DeleteNotificationChannel(ctx, db, id, user); err != nil {
		notificationError(c, err)
		return
	}
	c.Status(204)
}
func (h *NotificationHandler) TestChannel(c *gin.Context) {
	id, ok := notificationUUID(c)
	if !ok {
		return
	}
	ctx, cancel, db, user := notificationContext(c)
	defer cancel()
	channel, err := models.GetNotificationChannel(ctx, db, id, user)
	if err != nil {
		notificationError(c, err)
		return
	}
	if !channel.Enabled {
		notificationError(c, models.ErrNotificationDisabled)
		return
	}
	if err = services.ValidateNotificationConfig(channel.Type, &channel.Config); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	payload, _ := json.Marshal(gin.H{"status": "test", "rule": "Notification test", "server": "Server Monitor", "message": "This is a test notification from Server Monitor.", "timestamp": time.Now().UTC()})
	ids, err := models.EnqueueUserNotification(ctx, db, user, []uuid.UUID{id}, "test", payload)
	if err != nil {
		notificationError(c, err)
		return
	}
	c.JSON(202, gin.H{"delivery_id": ids[0], "message": "Test queued; check delivery history for the result"})
}
func (h *NotificationHandler) ListDeliveries(c *gin.Context) {
	limit := 50
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			c.JSON(400, gin.H{"error": "limit must be between 1 and 200"})
			return
		}
		limit = parsed
	}
	var channelID *uuid.UUID
	if value := c.Query("channel_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid channel ID"})
			return
		}
		channelID = &id
	}
	ctx, cancel, db, user := notificationContext(c)
	defer cancel()
	result, err := models.ListNotificationDeliveries(ctx, db, user, c.Query("status"), channelID, c.Query("cursor"), limit)
	if err != nil {
		notificationError(c, err)
		return
	}
	c.JSON(200, result)
}
func (h *NotificationHandler) RetryDelivery(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(400, gin.H{"error": "invalid delivery ID"})
		return
	}
	var req struct {
		ConfirmResend bool `json:"confirm_resend"`
	}
	if !readNotificationRequest(c, &req) {
		return
	}
	if !req.ConfirmResend {
		c.JSON(400, gin.H{"error": "confirm_resend must be true; this may send the notification again"})
		return
	}
	ctx, cancel, db, user := notificationContext(c)
	defer cancel()
	next, err := models.RetryNotificationDelivery(ctx, db, id, user)
	if err != nil {
		notificationError(c, err)
		return
	}
	c.JSON(202, gin.H{"delivery_id": next, "message": "A new delivery was queued; the recipient may receive a duplicate"})
}
