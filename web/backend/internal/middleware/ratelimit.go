package middleware

import (
	"net"
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

// rateLimitKey buckets IPv6 clients by their /64 prefix: a single subscriber
// typically controls that whole range, so per-address buckets would let the
// limit be dodged by rotating through it. IPv4 buckets stay per address.
func rateLimitKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() != nil {
		return ip
	}
	return (&net.IPNet{IP: parsed.Mask(net.CIDRMask(64, 128)), Mask: net.CIDRMask(64, 128)}).String()
}

// RateLimit returns a token-bucket rate limiter middleware.
// requests: max number of requests in the given duration per client IP.
func RateLimit(requests int, per time.Duration) gin.HandlerFunc {
	var mu sync.Mutex
	buckets := make(map[string]*bucket)

	// The map is bounded so a flood of one-shot IPs can't grow it without
	// limit between cleanups. At the cap, stale entries are pruned inline; if
	// it is still full, new keys are rejected — which is the limiter's job
	// anyway. Known keys always keep working.
	const maxBuckets = 10000

	pruneStale := func(now time.Time) {
		cutoff := now.Add(-10 * time.Minute)
		for ip, b := range buckets {
			if b.lastAccess.Before(cutoff) {
				delete(buckets, ip)
			}
		}
	}

	// Periodic cleanup of stale buckets (every 5 minutes, remove entries idle > 10 minutes)
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			mu.Lock()
			pruneStale(time.Now())
			mu.Unlock()
		}
	}()

	return func(c *gin.Context) {
		key := rateLimitKey(c.ClientIP())
		mu.Lock()
		b, ok := buckets[key]
		if !ok {
			if len(buckets) >= maxBuckets {
				pruneStale(time.Now())
			}
			if len(buckets) >= maxBuckets {
				mu.Unlock()
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
				return
			}
			b = &bucket{tokens: requests, lastFill: time.Now(), lastAccess: time.Now()}
			buckets[key] = b
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
