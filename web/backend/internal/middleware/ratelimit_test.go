package middleware

import "testing"

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
