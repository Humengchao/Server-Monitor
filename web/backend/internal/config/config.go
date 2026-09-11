package config

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL    string
	JWTSecret      string
	EncryptionKey  string
	ServerPort     string
	TLSCertFile    string
	TLSKeyFile     string
	CORSOrigin     string
	PollInterval   int // seconds between metrics polls
	AlertInterval  int // seconds between alert rule evaluations
	TrustedProxies []string
	// AllowPrivateWebhooks permits alert webhooks that resolve to loopback or
	// RFC1918 addresses. Off by default so an authenticated user cannot use the
	// alerting pipeline to reach the server's private network.
	AllowPrivateWebhooks bool
	// AllowRegistration gates POST /api/auth/register. On by default so existing
	// deployments keep their behavior; set ALLOW_REGISTRATION=false to close
	// signup once the initial accounts exist.
	AllowRegistration bool
}

// defaultTrustedProxies covers loopback and RFC1918 ranges, matching the
// docker-compose deployment where nginx proxies API traffic from a private
// network. X-Forwarded-For from public addresses is ignored so rate limiting
// and login history can't be spoofed by direct clients.
var defaultTrustedProxies = []string{
	"127.0.0.1/32", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
}

func Load() (*Config, error) {
	dbURL, ok := os.LookupEnv("DATABASE_URL")
	if !ok {
		return nil, errors.New("DATABASE_URL is required")
	}
	jwtSecret, ok := os.LookupEnv("JWT_SECRET")
	if !ok {
		return nil, errors.New("JWT_SECRET is required")
	}
	if len(jwtSecret) < 32 {
		// Warn but don't refuse: existing deployments with short secrets must
		// keep booting. Anything under 32 bytes is below the HS256 key size
		// recommended by RFC 7518 and is realistically brute-forceable.
		log.Printf("WARNING: JWT_SECRET is only %d bytes long; tokens signed with a key this small are weak. Use at least 32 random bytes (e.g. `openssl rand -base64 48`)", len(jwtSecret))
	}
	encKey, ok := os.LookupEnv("ENCRYPTION_KEY")
	if !ok {
		return nil, errors.New("ENCRYPTION_KEY is required")
	}
	if len(encKey) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be exactly 32 bytes")
	}
	pollInterval, err := loadInterval("POLL_INTERVAL", 3)
	if err != nil {
		return nil, err
	}
	alertInterval, err := loadInterval("ALERT_INTERVAL", 30)
	if err != nil {
		return nil, err
	}
	trustedProxies, err := loadTrustedProxies()
	if err != nil {
		return nil, err
	}
	allowRegistration, err := loadBoolDefault("ALLOW_REGISTRATION", true)
	if err != nil {
		return nil, err
	}
	return &Config{
		DatabaseURL:          dbURL,
		JWTSecret:            jwtSecret,
		EncryptionKey:        encKey,
		ServerPort:           getEnv("SERVER_PORT", "8080"),
		TLSCertFile:          os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:           os.Getenv("TLS_KEY_FILE"),
		CORSOrigin:           getEnv("CORS_ORIGIN", "http://localhost:5173"),
		PollInterval:         pollInterval,
		AlertInterval:        alertInterval,
		TrustedProxies:       trustedProxies,
		AllowPrivateWebhooks: strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_PRIVATE_WEBHOOKS")), "true"),
		AllowRegistration:    allowRegistration,
	}, nil
}

// loadBoolDefault parses a boolean switch. Empty keeps the fallback; accepted
// spellings are strconv.ParseBool's (true/false/1/0/t/f, any case). Anything
// else is a configuration error rather than a silent fallback — a typo on a
// security toggle must not quietly keep the feature in its default state.
func loadBoolDefault(key string, fallback bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean (true/false/1/0), got %q", key, v)
	}
	return b, nil
}

func loadInterval(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s must be a positive number of seconds, got %q", key, v)
	}
	return n, nil
}

// loadTrustedProxies parses TRUSTED_PROXIES as a comma-separated list of IPs
// or CIDRs. "none" disables proxy trust entirely (X-Forwarded-For is ignored).
func loadTrustedProxies() ([]string, error) {
	v := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES"))
	if v == "" {
		return defaultTrustedProxies, nil
	}
	if strings.EqualFold(v, "none") {
		return []string{}, nil
	}
	var proxies []string
	for _, entry := range strings.Split(v, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(entry); err != nil && net.ParseIP(entry) == nil {
			return nil, fmt.Errorf("TRUSTED_PROXIES entry %q is not an IP or CIDR", entry)
		}
		proxies = append(proxies, entry)
	}
	return proxies, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
