package config

import (
	"errors"
	"os"
)

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
	return &Config{
		DatabaseURL:   dbURL,
		JWTSecret:     jwtSecret,
		EncryptionKey: encKey,
		ServerPort:    getEnv("SERVER_PORT", "8080"),
		TLSCertFile:   os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:    os.Getenv("TLS_KEY_FILE"),
		CORSOrigin:    getEnv("CORS_ORIGIN", "http://localhost:5173"),
		PollInterval:  30,
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
