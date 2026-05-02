package handlers

import (
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
	// verify ownership
	if _, err := models.GetServerByIDAndUser(db, id, userID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	m, err := models.GetLatestMetric(db.Raw, id)
	if err != nil {
		c.JSON(http.StatusOK, nil)
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
	if _, err := models.GetServerByIDAndUser(db, id, userID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	since := time.Now().Add(-1 * time.Hour)
	if val := c.Query("since"); val != "" {
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			since = t
		}
	}
	until := time.Now()
	if val := c.Query("until"); val != "" {
		if t, err := time.Parse(time.RFC3339, val); err == nil {
			until = t
		}
	}
	points, err := models.GetMetrics(db.Raw, id, since, until)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load metrics"})
		return
	}
	if points == nil {
		points = []models.MetricPoint{}
	}
	c.JSON(http.StatusOK, points)
}
