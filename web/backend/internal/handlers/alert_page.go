package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/models"
)

func (h *AlertHandler) ListEventPage(c *gin.Context) {
	state := c.DefaultQuery("state", "all")
	if state != "all" && state != "active" && state != "resolved" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid alert state"})
		return
	}
	limit := 100
	if raw := c.Query("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 200"})
			return
		}
	}
	db := c.MustGet("db").(*models.DB)
	page, err := models.GetAlertEventPage(c.Request.Context(), db.Raw, c.MustGet("user_id").(uuid.UUID), state, limit, c.Query("cursor"))
	if errors.Is(err, models.ErrAlertCursor) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid alert cursor"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load alert events"})
		return
	}
	c.JSON(http.StatusOK, page)
}
