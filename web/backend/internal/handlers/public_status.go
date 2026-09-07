package handlers

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
)

// publicStatusCacheTTL bounds how often the status query runs regardless of
// visitor count. Metrics only change once per poll interval anyway.
const publicStatusCacheTTL = 2 * time.Second

// minPublicAvailabilityBuckets is four hours of fifteen-minute buckets. Under
// that, an availability percentage is noise.
const minPublicAvailabilityBuckets = 16

type PublicStatusHandler struct {
	mu      sync.Mutex
	cached  gin.H
	expires time.Time
}

func NewPublicStatusHandler() *PublicStatusHandler { return &PublicStatusHandler{} }

type publicNode struct {
	Alias             string             `json:"alias"`
	Name              string             `json:"name"`
	Location          string             `json:"location"`
	Tags              []models.PublicTag `json:"tags"`
	ServerType        string             `json:"server_type"`
	Status            string             `json:"status"`
	CPUCores          int                `json:"cpu_cores"`
	CPUPercent        int                `json:"cpu_percent"`
	Load1             float64            `json:"load_1"`
	Load5             float64            `json:"load_5"`
	Load15            float64            `json:"load_15"`
	MemoryUsed        int64              `json:"memory_used"`
	MemoryTotal       int64              `json:"memory_total"`
	MemoryPercent     int                `json:"memory_percent"`
	DiskUsed          int64              `json:"disk_used"`
	DiskTotal         int64              `json:"disk_total"`
	DiskPercent       int                `json:"disk_percent"`
	NetworkRxBytes    int64              `json:"network_rx_bytes"`
	NetworkTxBytes    int64              `json:"network_tx_bytes"`
	NetworkRxTotal    int64              `json:"network_rx_total_bytes"`
	NetworkTxTotal    int64              `json:"network_tx_total_bytes"`
	TrafficLimit      int64              `json:"traffic_limit_bytes"`
	TrafficPercent    int                `json:"traffic_percent"`
	UptimeSeconds     int64              `json:"uptime_seconds"`
	ExpiresAt         *time.Time         `json:"expires_at"`
	RemainingDays     int                `json:"remaining_days"`
	BillingPrice      float64            `json:"billing_price"`
	BillingCurrency   string             `json:"billing_currency"`
	BillingCycle      string             `json:"billing_cycle"`
	RemainingValue    float64            `json:"remaining_value"`
	LatencyMS         int                `json:"latency_ms"`
	PacketLossPercent int                `json:"packet_loss_percent"`
	// Observed availability over the last 30 days, clamped to how long the node
	// has existed. Null when the node is too new to have a meaningful figure.
	Availability30d *float64 `json:"availability_30d"`
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
	h.mu.Lock()
	if h.cached != nil && time.Now().Before(h.expires) {
		cached := h.cached
		h.mu.Unlock()
		c.JSON(http.StatusOK, cached)
		return
	}
	h.mu.Unlock()

	now := time.Now().UTC()
	// Snapped to the fifteen-minute tier the SLA figure is counted from, so the
	// still-open bucket is excluded from both the count and the expectation.
	availabilityEnd := services.TierEnd(now, models.DailyStripBucketSeconds)
	db := c.MustGet("db").(*models.DB)
	metrics, err := models.GetPublicServerMetrics(db.Raw, availabilityEnd.Add(-30*24*time.Hour), availabilityEnd)
	if err != nil {
		log.Printf("public status error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "status temporarily unavailable"})
		return
	}

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

		// Below a few hours of history the number says more about how recently
		// the node was added than about its reliability, so publish nothing.
		var availability *float64
		if metric.Availability30dExpected >= minPublicAvailabilityBuckets {
			value := services.AvailabilityPercent(metric.Availability30dObserved, metric.Availability30dExpected)
			availability = &value
		}

		nodes = append(nodes, publicNode{
			Alias: fmt.Sprintf("NODE %02d", index+1), Name: metric.Name, Location: metric.PublicLocation, Tags: metric.Tags,
			ServerType: metric.ServerType, Status: status,
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
			Availability30d: availability,
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

	response := gin.H{
		"overall": overall, "generated_at": now, "summary": summary, "nodes": nodes,
		"privacy": gin.H{
			"anonymized":    true,
			"hidden_fields": []string{"hostname", "ip_address", "port", "ssh_user", "credentials", "notes", "database_id"},
		},
	}

	// The response is read-only once built, so it is safe to share between
	// requests until the TTL expires.
	h.mu.Lock()
	h.cached = response
	h.expires = time.Now().Add(publicStatusCacheTTL)
	h.mu.Unlock()

	c.JSON(http.StatusOK, response)
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
