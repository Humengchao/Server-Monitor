package handlers

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
)

type PublicStatusHandler struct{}

func NewPublicStatusHandler() *PublicStatusHandler { return &PublicStatusHandler{} }

type publicNode struct {
	Alias         string `json:"alias"`
	ServerType    string `json:"server_type"`
	Status        string `json:"status"`
	CPUCores      int    `json:"cpu_cores"`
	CPUPercent    int    `json:"cpu_percent"`
	MemoryPercent int    `json:"memory_percent"`
	UptimeDays    int64  `json:"uptime_days"`
}

type publicSummary struct {
	Total    int `json:"total"`
	Online   int `json:"online"`
	Degraded int `json:"degraded"`
	Offline  int `json:"offline"`
}

func (h *PublicStatusHandler) Get(c *gin.Context) {
	db := c.MustGet("db").(*models.DB)
	metrics, err := models.GetPublicServerMetrics(db.Raw)
	if err != nil {
		log.Printf("public status error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "status temporarily unavailable"})
		return
	}

	now := time.Now().UTC()
	nodes := make([]publicNode, 0, len(metrics))
	summary := publicSummary{Total: len(metrics)}

	for index, metric := range metrics {
		memoryPercent := 0
		if metric.MemoryTotal > 0 {
			memoryPercent = clampPercent(float64(metric.MemoryUsed) / float64(metric.MemoryTotal) * 100)
		}
		cpuPercent := clampPercent(metric.CPUPercent)
		status := "offline"
		if metric.RecordedAt != nil && now.Sub(*metric.RecordedAt) < 2*time.Minute {
			status = "online"
			if cpuPercent >= 85 || memoryPercent >= 90 {
				status = "degraded"
			}
		}

		switch status {
		case "online":
			summary.Online++
		case "degraded":
			summary.Degraded++
		default:
			summary.Offline++
		}

		nodes = append(nodes, publicNode{
			Alias:         fmt.Sprintf("NODE %02d", index+1),
			ServerType:    metric.ServerType,
			Status:        status,
			CPUCores:      metric.CPUCores,
			CPUPercent:    cpuPercent,
			MemoryPercent: memoryPercent,
			UptimeDays:    metric.UptimeSeconds / 86400,
		})
	}

	overall := "operational"
	if summary.Total > 0 && summary.Online == 0 && summary.Degraded == 0 {
		overall = "outage"
	} else if summary.Degraded > 0 || summary.Offline > 0 {
		overall = "degraded"
	}

	c.JSON(http.StatusOK, gin.H{
		"overall":      overall,
		"generated_at": now,
		"summary":      summary,
		"nodes":        nodes,
		"privacy": gin.H{
			"anonymized": true,
			"hidden_fields": []string{
				"hostname", "ip_address", "port", "ssh_user", "credentials", "notes", "database_id",
			},
		},
	})
}

func clampPercent(value float64) int {
	return max(0, min(100, int(math.Round(value))))
}
