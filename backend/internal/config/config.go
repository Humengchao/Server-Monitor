package config

import (
	"errors"
	"fmt"
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
	TrustedProxies []string
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
	encKey, ok := os.LookupEnv("ENCRYPTION_KEY")
	if !ok {
		return nil, errors.New("ENCRYPTION_KEY is required")
	}
	if len(encKey) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be exactly 32 bytes")
	}
	pollInterval, err := loadPollInterval()
	if err != nil {
		return nil, err
	}
	trustedProxies, err := loadTrustedProxies()
	if err != nil {
		return nil, err
	}
	return &Config{
		DatabaseURL:    dbURL,
		JWTSecret:      jwtSecret,
		EncryptionKey:  encKey,
		ServerPort:     getEnv("SERVER_PORT", "8080"),
		TLSCertFile:    os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:     os.Getenv("TLS_KEY_FILE"),
		CORSOrigin:     getEnv("CORS_ORIGIN", "http://localhost:5173"),
		PollInterval:   pollInterval,
		TrustedProxies: trustedProxies,
	}, nil
}

func loadPollInterval() (int, error) {
	v := os.Getenv("POLL_INTERVAL")
	if v == "" {
		return 3, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("POLL_INTERVAL must be a positive number of seconds, got %q", v)
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
