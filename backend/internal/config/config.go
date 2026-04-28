package config

import "os"

type Config struct {
	DatabaseURL  string
	JWTSecret    string
	ServerPort   string
	PollInterval int // seconds between metrics polls
}

func Load() *Config {
	return &Config{
		DatabaseURL:  getEnv("DATABASE_URL", "postgres://monitor:monitor123@localhost:5432/server_monitor?sslmode=disable"),
		JWTSecret:    getEnv("JWT_SECRET", "server-monitor-secret-change-me"),
		ServerPort:   getEnv("SERVER_PORT", "8080"),
		PollInterval: 30,
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
