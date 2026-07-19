package main

import (
	"bufio"
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"server-monitor/internal/config"
	"server-monitor/internal/database"
	"server-monitor/internal/models"
	"server-monitor/internal/router"
	"server-monitor/internal/services"
)

func main() {
	loadEnv()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	defer db.Close()

	// Run database migrations before starting any service
	if err := database.RunMigrations(db); err != nil {
		log.Fatalf("Database migration failed: %v", err)
	}
	log.Println("Database migrations completed")

	// Start metrics collector
	dbWrapper := &models.DB{Raw: db, EncryptionKey: cfg.EncryptionKey}
	collector := services.NewCollector(dbWrapper, time.Duration(cfg.PollInterval)*time.Second)
	collector.Start()

	log.Printf("Server starting on :%s", cfg.ServerPort)
	r := router.Setup(db, cfg)

	// Create http.Server for graceful shutdown support
	srv := &http.Server{
		Addr:    ":" + cfg.ServerPort,
		Handler: r,
	}

	// Start server in goroutine
	go func() {
		var err error
		if cfg.TLSCertFile != "" && cfg.TLSKeyFile != "" {
			log.Printf("TLS enabled, cert=%s key=%s", cfg.TLSCertFile, cfg.TLSKeyFile)
			err = srv.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	collector.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}
	log.Println("Server exited gracefully")
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
