package handlers

import (
	"database/sql"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"math"
	"net/http"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
	"strings"
)

type NetworkHandler struct {
	monitor      *services.NetworkMonitor
	historyCache *networkTrendCache
}

func NewNetworkHandler(m *services.NetworkMonitor) *NetworkHandler {
	return &NetworkHandler{monitor: m, historyCache: newNetworkTrendCache()}
}
func networkDB(c *gin.Context) (*sql.DB, uuid.UUID) {
	return c.MustGet("db").(*models.DB).Raw, c.MustGet("user_id").(uuid.UUID)
}
func networkID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("check_id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "invalid check id"})
		return id, false
	}
	return id, true
}
func (h *NetworkHandler) List(c *gin.Context) {
	db, user := networkDB(c)
	checks, err := models.ListNetworkChecks(c.Request.Context(), db, &user, false)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to read checks"})
		return
	}
	c.JSON(200, checks)
}
func (h *NetworkHandler) Create(c *gin.Context) { h.save(c, true) }
func (h *NetworkHandler) Update(c *gin.Context) { h.save(c, false) }
func (h *NetworkHandler) save(c *gin.Context, create bool) {
	var check models.NetworkCheck
	if c.ShouldBindJSON(&check) != nil {
		c.JSON(400, gin.H{"error": "invalid check"})
		return
	}
	check.Name = strings.TrimSpace(check.Name)
	if check.Name == "" || len(check.Name) > 128 || check.TargetServerID == uuid.Nil || check.Packets < 1 || check.Packets > 10 || math.IsNaN(check.LossThreshold) || math.IsInf(check.LossThreshold, 0) || check.LossThreshold < 0 || check.LossThreshold > 100 || check.LatencyThresholdMS != nil && (*check.LatencyThresholdMS < 0 || *check.LatencyThresholdMS > 60000 || math.IsNaN(*check.LatencyThresholdMS)) {
		c.JSON(400, gin.H{"error": "invalid check parameters"})
		return
	}
	db, user := networkDB(c)
	check.UserID = user
	check.ID = uuid.New()
	if !create {
		var ok bool
		check.ID, ok = networkID(c)
		if !ok {
			return
		}
	}
	err := models.SaveNetworkCheck(c.Request.Context(), db, &check, create)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(404, gin.H{"error": "check or assigned server not found"})
		return
	}
	if errors.Is(err, models.ErrNetworkLimit) {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to save check"})
		return
	}
	check, err = models.GetNetworkCheck(c.Request.Context(), db, check.ID, user)
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to read saved check"})
		return
	}
	c.JSON(200, check)
}
func (h *NetworkHandler) Delete(c *gin.Context) {
	id, ok := networkID(c)
	if !ok {
		return
	}
	db, user := networkDB(c)
	err := models.DeleteNetworkCheck(c.Request.Context(), db, id, user)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(404, gin.H{"error": "check not found"})
		return
	}
	if err != nil {
		c.JSON(500, gin.H{"error": "failed to delete check"})
		return
	}
	c.Status(http.StatusNoContent)
}
func (h *NetworkHandler) Run(c *gin.Context) {
	if h.monitor == nil {
		c.JSON(503, gin.H{"error": "network monitor unavailable"})
		return
	}
	id, ok := networkID(c)
	if !ok {
		return
	}
	_, user := networkDB(c)
	sample, err := h.monitor.Run(c.Request.Context(), user, id)
	if errors.Is(err, services.ErrNetworkBusy) {
		c.JSON(409, gin.H{"error": "check already running"})
		return
	}
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(404, gin.H{"error": "check not found"})
		return
	}
	if err != nil {
		c.JSON(502, gin.H{"error": "probe could not complete"})
		return
	}
	c.JSON(200, sample)
}
