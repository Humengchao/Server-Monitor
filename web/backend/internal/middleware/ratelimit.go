package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type bucket struct {
	tokens     int
	lastFill   time.Time
	lastAccess time.Time
}

// RateLimit returns a token-bucket rate limiter middleware.
// requests: max number of requests in the given duration per client IP.
func RateLimit(requests int, per time.Duration) gin.HandlerFunc {
	var mu sync.Mutex
	buckets := make(map[string]*bucket)

	// Periodic cleanup of stale buckets (every 5 minutes, remove entries idle > 10 minutes)
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for ip, b := range buckets {
				if b.lastAccess.Before(cutoff) {
					delete(buckets, ip)
				}
			}
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		ip := c.ClientIP()
		mu.Lock()
		b, ok := buckets[ip]
		if !ok {
			b = &bucket{tokens: requests, lastFill: time.Now(), lastAccess: time.Now()}
			buckets[ip] = b
		}
		// refill
		now := time.Now()
		elapsed := now.Sub(b.lastFill)
		refill := int(float64(requests) * elapsed.Seconds() / per.Seconds())
		if refill > 0 {
			b.tokens += refill
			if b.tokens > requests {
				b.tokens = requests
			}
			b.lastFill = now
		}
		b.lastAccess = time.Now()
		if b.tokens > 0 {
			b.tokens--
			mu.Unlock()
			c.Next()
			return
		}
		mu.Unlock()
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
	}
}
