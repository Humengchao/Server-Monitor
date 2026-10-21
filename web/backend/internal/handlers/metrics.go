package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type MetricsHandler struct{}

func NewMetricsHandler() *MetricsHandler { return &MetricsHandler{} }

func (h *MetricsHandler) GetLatest(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	// Authorization must not depend on SSH credentials being decryptable.
	owned, err := models.ServerOwnedByUserContext(c.Request.Context(), db.Raw, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify server ownership"})
		return
	}
	if !owned {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	m, err := models.GetLatestMetric(c.Request.Context(), db.Raw, id)
	// A new server has no samples yet. Database failures must stay distinguishable
	// so clients retain their last reading and show the failed refresh.
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusOK, nil)
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load metrics"})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *MetricsHandler) GetHistory(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	owned, err := models.ServerOwnedByUserContext(c.Request.Context(), db.Raw, id, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify server ownership"})
		return
	}
	if !owned {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	now := time.Now()
	until := now
	if val := c.Query("until"); val != "" {
		until, err = time.Parse(time.RFC3339Nano, val)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid until timestamp"})
			return
		}
	}
	since := until.Add(-time.Hour)
	if val := c.Query("since"); val != "" {
		since, err = time.Parse(time.RFC3339Nano, val)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid since timestamp"})
			return
		}
	}
	if !since.Before(until) || until.Sub(since) > models.MaxMetricHistoryWindow || until.After(now.Add(5*time.Minute)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "history range must be positive, at most 366 days, and not in the future"})
		return
	}
	points, err := models.GetMetrics(c.Request.Context(), db.Raw, id, since, until)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load metrics"})
		return
	}
	if points == nil {
		points = []models.MetricPoint{}
	}
	c.JSON(http.StatusOK, points)
}
