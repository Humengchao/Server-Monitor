package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

type Config struct {
	PublicURL      string
	AgentAssetsDir string
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
	AllowPrivateWebhooks    bool
	AIAllowPrivateEndpoints bool
	// Migration-only escape hatch. The default requires a trusted, pinned key.
	SSHAllowUnverifiedHostKeys bool
	// AllowRegistration gates POST /api/auth/register. On by default so existing
	// deployments keep their behavior; set ALLOW_REGISTRATION=false to close
	// signup once the initial accounts exist.
	AllowRegistration bool
	AutoRenewExpiry   bool
	ExpiryTimezone    *time.Location
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
	if err := validateJWTSecret(jwtSecret); err != nil {
		return nil, err
	}
	encKey, ok := os.LookupEnv("ENCRYPTION_KEY")
	if !ok {
		return nil, errors.New("ENCRYPTION_KEY is required")
	}
	if len(encKey) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be exactly 32 bytes")
	}
	if jwtSecret == encKey {
		return nil, errors.New("JWT_SECRET must be independent of ENCRYPTION_KEY")
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
	autoRenewExpiry, err := loadBoolDefault("AUTO_RENEW_EXPIRY", true)
	if err != nil {
		return nil, err
	}
	aiPrivateEndpoints, err := loadBoolDefault("AI_ALLOW_PRIVATE_ENDPOINTS", false)
	if err != nil {
		return nil, err
	}
	unverifiedHostKeys, err := loadBoolDefault("SSH_ALLOW_UNVERIFIED_HOST_KEYS", false)
	if err != nil {
		return nil, err
	}
	expiryTimezone, err := time.LoadLocation(getEnv("EXPIRY_TIMEZONE", "Asia/Shanghai"))
	if err != nil {
		return nil, fmt.Errorf("EXPIRY_TIMEZONE: %w", err)
	}
	return &Config{
		PublicURL:                  strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"),
		AgentAssetsDir:             getEnv("AGENT_ASSETS_DIR", "/app/agent-assets"),
		DatabaseURL:                dbURL,
		JWTSecret:                  jwtSecret,
		EncryptionKey:              encKey,
		ServerPort:                 getEnv("SERVER_PORT", "8080"),
		TLSCertFile:                os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:                 os.Getenv("TLS_KEY_FILE"),
		CORSOrigin:                 getEnv("CORS_ORIGIN", "http://localhost:5173"),
		PollInterval:               pollInterval,
		AlertInterval:              alertInterval,
		TrustedProxies:             trustedProxies,
		AllowPrivateWebhooks:       strings.EqualFold(strings.TrimSpace(os.Getenv("ALLOW_PRIVATE_WEBHOOKS")), "true"),
		AIAllowPrivateEndpoints:    aiPrivateEndpoints,
		SSHAllowUnverifiedHostKeys: unverifiedHostKeys,
		AllowRegistration:          allowRegistration,
		AutoRenewExpiry:            autoRenewExpiry,
		ExpiryTimezone:             expiryTimezone,
	}, nil
}

func validateJWTSecret(secret string) error {
	invalid := errors.New("JWT_SECRET must be at least 32 random bytes; generate a new independent secret instead of using a public example")
	if len(secret) < 32 || strings.TrimSpace(secret) != secret {
		return invalid
	}
	lower := strings.ToLower(secret)
	for _, marker := range []string{"change-me", "changeme", "your-secret", "replace-me", "0123456789abcdef", "abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(lower, marker) {
			return invalid
		}
	}
	if strings.Trim(secret, string(secret[0])) == "" {
		return invalid
	}
	return nil
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
