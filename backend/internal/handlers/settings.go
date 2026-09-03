package handlers

import (
	"net/http"
	"strings"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// SettingsHandler exposes per-user preferences.
type SettingsHandler struct {
	notifier *services.WebhookNotifier
}

func NewSettingsHandler(notifier *services.WebhookNotifier) *SettingsHandler {
	return &SettingsHandler{notifier: notifier}
}

func (h *SettingsHandler) Get(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	settings, err := models.GetUserSettings(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}

func (h *SettingsHandler) Update(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req struct {
		DefaultWebhookURL string `json:"default_webhook_url"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.DefaultWebhookURL = strings.TrimSpace(req.DefaultWebhookURL)
	// Empty clears the default. A non-empty value goes through the same
	// SSRF check as a per-rule webhook — saving it here would otherwise be a
	// way around the validation the rule form applies.
	if req.DefaultWebhookURL != "" {
		if err := h.notifier.ValidateWebhookURL(req.DefaultWebhookURL); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.SaveUserSettings(db.Raw, userID, models.UserSettings{
		DefaultWebhookURL: req.DefaultWebhookURL,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save settings"})
		return
	}
	settings, err := models.GetUserSettings(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load settings"})
		return
	}
	c.JSON(http.StatusOK, settings)
}
