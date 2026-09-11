package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRateLimitKey(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want string
	}{
		{name: "IPv4 stays per address", ip: "203.0.113.10", want: "203.0.113.10"},
		{name: "IPv4 private stays per address", ip: "192.168.1.5", want: "192.168.1.5"},
		{name: "IPv6 buckets to /64", ip: "2001:db8:85a3::8a2e:370:7334", want: "2001:db8:85a3::/64"},
		{name: "same /64 shares the bucket", ip: "2001:db8:85a3:0:ffff:ffff:ffff:ffff", want: "2001:db8:85a3::/64"},
		{name: "IPv6 loopback buckets to /64", ip: "::1", want: "::/64"},
		{name: "IPv4-mapped IPv6 counts as IPv4", ip: "::ffff:192.168.1.5", want: "::ffff:192.168.1.5"},
		{name: "unparseable key passes through", ip: "not-an-ip", want: "not-an-ip"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rateLimitKey(tc.ip); got != tc.want {
				t.Fatalf("rateLimitKey(%q) = %q, want %q", tc.ip, got, tc.want)
			}
		})
	}
}

func TestRateLimitCapRejectsNewKeys(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", RateLimit(2, time.Minute), func(c *gin.Context) { c.Status(http.StatusOK) })

	serve := func(remoteAddr string) int {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remoteAddr
		r.ServeHTTP(w, req)
		return w.Code
	}

	// Fill the bucket map to its 10000-key cap with one-shot addresses.
	for i := 0; i < 10000; i++ {
		addr := fmt.Sprintf("10.%d.%d.%d:8080", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
		if code := serve(addr); code != http.StatusOK {
			t.Fatalf("request %d from %s: status = %d, want %d", i, addr, code, http.StatusOK)
		}
	}

	// A known key is unaffected by the cap: its second request still has a token.
	if code := serve("10.0.0.0:8080"); code != http.StatusOK {
		t.Fatalf("repeat request from a known key: status = %d, want %d", code, http.StatusOK)
	}
	// A brand-new key is rejected while the map stays full.
	if code := serve("10.255.255.255:8080"); code != http.StatusTooManyRequests {
		t.Fatalf("request from a new key at capacity: status = %d, want %d", code, http.StatusTooManyRequests)
	}
}
