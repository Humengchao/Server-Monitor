package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AlertHandler struct {
	notifier *services.WebhookNotifier
}

func NewAlertHandler(notifier *services.WebhookNotifier) *AlertHandler {
	return &AlertHandler{notifier: notifier}
}

type AlertRuleRequest struct {
	Name       string     `json:"name" binding:"required"`
	ServerID   *uuid.UUID `json:"server_id"`
	Metric     string     `json:"metric" binding:"required"`
	Comparator string     `json:"comparator"`
	Threshold  float64    `json:"threshold"`
	Duration   int        `json:"duration_seconds"`
	Enabled    *bool      `json:"enabled"`
	WebhookURL string     `json:"webhook_url"`
}

const (
	alertMinDuration = 30
	alertMaxDuration = 24 * 60 * 60
)

// normalizeAlertRule validates user input and fills in the defaults the engine
// expects. Percent metrics are clamped to 0-100 so a rule can never be written
// in a way that makes it impossible (or trivial) to satisfy.
func normalizeAlertRule(req *AlertRuleRequest) error {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return fmt.Errorf("rule name is required")
	}
	if len(req.Name) > 128 {
		return fmt.Errorf("rule name is too long")
	}
	req.Metric = strings.ToLower(strings.TrimSpace(req.Metric))
	if !models.ValidAlertMetric(req.Metric) {
		return fmt.Errorf("unsupported metric %q", req.Metric)
	}
	req.Comparator = strings.TrimSpace(req.Comparator)
	if req.Comparator == "" {
		req.Comparator = ">"
	}
	if req.Comparator != ">" && req.Comparator != "<" {
		return fmt.Errorf("comparator must be > or <")
	}
	switch req.Metric {
	case models.AlertMetricCPU, models.AlertMetricMemory, models.AlertMetricDisk:
		if req.Threshold < 0 || req.Threshold > 100 {
			return fmt.Errorf("threshold for %s must be between 0 and 100", req.Metric)
		}
	case models.AlertMetricOffline:
		// The threshold is unused; the duration alone defines the rule.
		req.Threshold = 0
		req.Comparator = ">"
	default:
		if req.Threshold < 0 {
			return fmt.Errorf("threshold must not be negative")
		}
	}
	if req.Duration == 0 {
		req.Duration = 300
	}
	if req.Duration < alertMinDuration || req.Duration > alertMaxDuration {
		return fmt.Errorf("duration must be between %d and %d seconds", alertMinDuration, alertMaxDuration)
	}
	req.WebhookURL = strings.TrimSpace(req.WebhookURL)
	return nil
}

func (h *AlertHandler) ListRules(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	rules, err := models.GetAlertRulesByUserID(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load alert rules"})
		return
	}
	if rules == nil {
		rules = []models.AlertRule{}
	}
	c.JSON(http.StatusOK, rules)
}

func (h *AlertHandler) CreateRule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req AlertRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := normalizeAlertRule(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.notifier.ValidateWebhookURL(req.WebhookURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule := &models.AlertRule{
		UserID:     userID,
		Name:       req.Name,
		ServerID:   req.ServerID,
		Metric:     req.Metric,
		Comparator: req.Comparator,
		Threshold:  req.Threshold,
		Duration:   req.Duration,
		Enabled:    enabled,
		WebhookURL: req.WebhookURL,
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.CreateAlertRule(db.Raw, rule); err != nil {
		// The insert is guarded by an ownership check, so no rows means the
		// referenced server belongs to somebody else (or does not exist).
		if err == sql.ErrNoRows {
			c.JSON(http.StatusBadRequest, gin.H{"error": "server not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create alert rule"})
		return
	}
	c.JSON(http.StatusCreated, rule)
}

func (h *AlertHandler) UpdateRule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req AlertRuleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := normalizeAlertRule(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.notifier.ValidateWebhookURL(req.WebhookURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rule := &models.AlertRule{
		ID:         id,
		UserID:     userID,
		Name:       req.Name,
		ServerID:   req.ServerID,
		Metric:     req.Metric,
		Comparator: req.Comparator,
		Threshold:  req.Threshold,
		Duration:   req.Duration,
		Enabled:    enabled,
		WebhookURL: req.WebhookURL,
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.UpdateAlertRule(db.Raw, rule); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "alert rule not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update alert rule"})
		return
	}
	c.JSON(http.StatusOK, rule)
}

func (h *AlertHandler) DeleteRule(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.DeleteAlertRule(db.Raw, id, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete alert rule"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *AlertHandler) ListEvents(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	activeOnly := c.Query("active") == "1" || c.Query("active") == "true"
	limit, _ := strconv.Atoi(c.Query("limit"))
	events, err := models.GetAlertEventsByUserID(db.Raw, userID, activeOnly, limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load alert events"})
		return
	}
	if events == nil {
		events = []models.AlertEvent{}
	}
	c.JSON(http.StatusOK, events)
}

// Summary powers the header badge without transferring the whole event list.
func (h *AlertHandler) Summary(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	count, err := models.CountActiveAlerts(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load alert summary"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"active": count})
}

// TestWebhook delivers a sample payload so a user can verify their endpoint
// before waiting for a real threshold breach.
func (h *AlertHandler) TestWebhook(c *gin.Context) {
	var req struct {
		WebhookURL string `json:"webhook_url" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.notifier.ValidateWebhookURL(req.WebhookURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	payload := gin.H{
		"status":    "test",
		"rule":      "Webhook test",
		"server":    "example-host",
		"metric":    "cpu",
		"value":     93.4,
		"threshold": 90,
		"message":   "This is a test notification from Server Monitor.",
		"timestamp": time.Now(),
	}
	if err := h.notifier.Post(c.Request.Context(), req.WebhookURL, payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "sent"})
}
