package main

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"server-monitor/internal/config"
	"server-monitor/internal/database"
	"server-monitor/internal/models"
	"server-monitor/internal/router"
	"server-monitor/internal/services"
)

func main() {
	loadEnv()

	cfg := config.Load()

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	defer db.Close()

	if err := database.RunMigrations(db); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	// Start metrics collector
	dbWrapper := &models.DB{Raw: db, EncryptionKey: cfg.EncryptionKey}
	collector := services.NewCollector(dbWrapper, time.Duration(cfg.PollInterval)*time.Second)
	collector.Start()

	log.Printf("Server starting on :%s", cfg.ServerPort)
	r := router.Setup(db, cfg)
	if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
		log.Printf("TLS enabled, cert=%s key=%s", cfg.TLSCertFile, cfg.TLSKeyFile)
		if err := r.RunTLS(":"+cfg.ServerPort, cfg.TLSCertFile, cfg.TLSKeyFile); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	} else {
		if err := r.Run(":" + cfg.ServerPort); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}
}

// loadEnv reads .env from the backend directory and sets env vars.
func loadEnv() {
	_, filename, _, _ := runtime.Caller(0)
	envFile := filepath.Join(filepath.Dir(filename), "..", "..", ".env")
	f, err := os.Open(envFile)
	if err != nil {
		// .env is optional
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
