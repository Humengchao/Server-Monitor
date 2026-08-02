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
	Alias             string     `json:"alias"`
	ServerType        string     `json:"server_type"`
	Status            string     `json:"status"`
	CPUCores          int        `json:"cpu_cores"`
	CPUPercent        int        `json:"cpu_percent"`
	Load1             float64    `json:"load_1"`
	Load5             float64    `json:"load_5"`
	Load15            float64    `json:"load_15"`
	MemoryUsed        int64      `json:"memory_used"`
	MemoryTotal       int64      `json:"memory_total"`
	MemoryPercent     int        `json:"memory_percent"`
	DiskUsed          int64      `json:"disk_used"`
	DiskTotal         int64      `json:"disk_total"`
	DiskPercent       int        `json:"disk_percent"`
	NetworkRxBytes    int64      `json:"network_rx_bytes"`
	NetworkTxBytes    int64      `json:"network_tx_bytes"`
	NetworkRxTotal    int64      `json:"network_rx_total_bytes"`
	NetworkTxTotal    int64      `json:"network_tx_total_bytes"`
	TrafficLimit      int64      `json:"traffic_limit_bytes"`
	TrafficPercent    int        `json:"traffic_percent"`
	UptimeSeconds     int64      `json:"uptime_seconds"`
	ExpiresAt         *time.Time `json:"expires_at"`
	RemainingDays     int        `json:"remaining_days"`
	BillingPrice      float64    `json:"billing_price"`
	BillingCurrency   string     `json:"billing_currency"`
	BillingCycle      string     `json:"billing_cycle"`
	RemainingValue    float64    `json:"remaining_value"`
	LatencyMS         int        `json:"latency_ms"`
	PacketLossPercent int        `json:"packet_loss_percent"`
}

type publicSummary struct {
	Total          int     `json:"total"`
	Online         int     `json:"online"`
	Degraded       int     `json:"degraded"`
	Offline        int     `json:"offline"`
	MemoryUsed     int64   `json:"memory_used"`
	MemoryTotal    int64   `json:"memory_total"`
	DiskUsed       int64   `json:"disk_used"`
	DiskTotal      int64   `json:"disk_total"`
	TrafficTotal   int64   `json:"traffic_total_bytes"`
	NetworkRxBytes int64   `json:"network_rx_bytes"`
	NetworkTxBytes int64   `json:"network_tx_bytes"`
	RemainingValue float64 `json:"remaining_value"`
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
		memoryPercent := percent(metric.MemoryUsed, metric.MemoryTotal)
		diskPercent := percent(metric.DiskUsed, metric.DiskTotal)
		trafficTotal := metric.NetworkRxTotal + metric.NetworkTxTotal
		trafficPercent := percent(trafficTotal, metric.TrafficLimit)
		cpuPercent := clampPercent(metric.CPUPercent)
		status := "offline"
		if metric.RecordedAt != nil && now.Sub(*metric.RecordedAt) < 2*time.Minute {
			status = "online"
			if cpuPercent >= 85 || memoryPercent >= 90 || diskPercent >= 90 {
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

		remainingDays, remainingValue := calculateRemaining(now, metric.ExpiresAt, metric.BillingPrice, metric.BillingCycle)
		packetLoss := 0
		if status == "offline" {
			packetLoss = 100
		}

		nodes = append(nodes, publicNode{
			Alias: fmt.Sprintf("NODE %02d", index+1), ServerType: metric.ServerType, Status: status,
			CPUCores: metric.CPUCores, CPUPercent: cpuPercent,
			Load1: metric.Load1, Load5: metric.Load5, Load15: metric.Load15,
			MemoryUsed: metric.MemoryUsed, MemoryTotal: metric.MemoryTotal, MemoryPercent: memoryPercent,
			DiskUsed: metric.DiskUsed, DiskTotal: metric.DiskTotal, DiskPercent: diskPercent,
			NetworkRxBytes: metric.NetworkRxBytes, NetworkTxBytes: metric.NetworkTxBytes,
			NetworkRxTotal: metric.NetworkRxTotal, NetworkTxTotal: metric.NetworkTxTotal,
			TrafficLimit: metric.TrafficLimit, TrafficPercent: trafficPercent,
			UptimeSeconds: metric.UptimeSeconds, ExpiresAt: metric.ExpiresAt, RemainingDays: remainingDays,
			BillingPrice: metric.BillingPrice, BillingCurrency: metric.BillingCurrency,
			BillingCycle: metric.BillingCycle, RemainingValue: remainingValue,
			LatencyMS: metric.LatencyMS, PacketLossPercent: packetLoss,
		})

		summary.MemoryUsed += metric.MemoryUsed
		summary.MemoryTotal += metric.MemoryTotal
		summary.DiskUsed += metric.DiskUsed
		summary.DiskTotal += metric.DiskTotal
		summary.TrafficTotal += trafficTotal
		summary.NetworkRxBytes += metric.NetworkRxBytes
		summary.NetworkTxBytes += metric.NetworkTxBytes
		summary.RemainingValue += remainingValue
	}

	overall := "operational"
	if summary.Total > 0 && summary.Online == 0 && summary.Degraded == 0 {
		overall = "outage"
	} else if summary.Degraded > 0 || summary.Offline > 0 {
		overall = "degraded"
	}

	c.JSON(http.StatusOK, gin.H{
		"overall": overall, "generated_at": now, "summary": summary, "nodes": nodes,
		"privacy": gin.H{
			"anonymized":    true,
			"hidden_fields": []string{"hostname", "ip_address", "port", "ssh_user", "credentials", "notes", "database_id", "real_name", "location"},
		},
	})
}

func percent(used, total int64) int {
	if total <= 0 {
		return 0
	}
	return clampPercent(float64(used) / float64(total) * 100)
}

func clampPercent(value float64) int {
	return max(0, min(100, int(math.Round(value))))
}

func calculateRemaining(now time.Time, expiresAt *time.Time, price float64, cycle string) (int, float64) {
	if expiresAt == nil || !expiresAt.After(now) {
		return 0, 0
	}
	days := int(math.Ceil(expiresAt.Sub(now).Hours() / 24))
	cycleDays := map[string]float64{"month": 30, "quarter": 91, "half_year": 182, "year": 365}[cycle]
	if cycleDays == 0 || price <= 0 {
		return days, 0
	}
	return days, math.Round(price*float64(days)/cycleDays*100) / 100
}
