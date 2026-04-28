package config

import "os"

type Config struct {
	DatabaseURL   string
	JWTSecret     string
	EncryptionKey string
	ServerPort    string
	TLSCertFile   string
	TLSKeyFile    string
	CORSOrigin    string
	PollInterval  int // seconds between metrics polls
}

func Load() *Config {
	cfg := &Config{
		DatabaseURL:   requireEnv("DATABASE_URL"),
		JWTSecret:     requireEnv("JWT_SECRET"),
		EncryptionKey: requireEnv("ENCRYPTION_KEY"),
		ServerPort:    getEnv("SERVER_PORT", "8080"),
		TLSCertFile:   os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:    os.Getenv("TLS_KEY_FILE"),
		CORSOrigin:    getEnv("CORS_ORIGIN", "http://localhost:5173"),
		PollInterval:  30,
	}
	if len(cfg.EncryptionKey) != 32 {
		panic("ENCRYPTION_KEY must be exactly 32 bytes")
	}
	return cfg
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic("missing required environment variable: " + key)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
