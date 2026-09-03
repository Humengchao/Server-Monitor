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

const (
	exitConfigError     = 10
	exitDatabaseError   = 11
	exitMigrationError  = 12
	exitHTTPServerError = 13
	exitShutdownError   = 14
)

func exitWithError(code int, message string, err error) {
	log.Printf("%s: %v", message, err)
	os.Exit(code)
}

func main() {
	loadEnv()

	cfg, err := config.Load()
	if err != nil {
		exitWithError(exitConfigError, "Config error", err)
	}

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		exitWithError(exitDatabaseError, "Database connection failed", err)
	}
	defer db.Close()

	// Configure connection pool (default is only 2 max open conns)
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Run database migrations before starting any service
	if err := database.RunMigrations(db); err != nil {
		exitWithError(exitMigrationError, "Database migration failed", err)
	}
	log.Println("Database migrations completed")

	// Start metrics collector
	dbWrapper := &models.DB{Raw: db, EncryptionKey: cfg.EncryptionKey}
	collector := services.NewCollector(dbWrapper, time.Duration(cfg.PollInterval)*time.Second)
	collector.Start()

	// Evaluate threshold alert rules independently of the collector so a slow
	// host cannot delay notifications for the rest of the fleet.
	notifier := services.NewWebhookNotifier(cfg.AllowPrivateWebhooks)
	alertEngine := services.NewAlertEngine(db, time.Duration(cfg.AlertInterval)*time.Second, notifier)
	alertEngine.Start()

	// Shared SSH connection cache for interactive API calls (Docker management).
	sshCache := services.NewSSHConnCache()

	log.Printf("Server starting on :%s", cfg.ServerPort)
	r, err := router.Setup(db, cfg, sshCache, notifier, collector)
	if err != nil {
		exitWithError(exitConfigError, "Router setup failed", err)
	}

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
			exitWithError(exitHTTPServerError, "Failed to start server", err)
		}
	}()

	// Wait for interrupt signal for graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	collector.Stop()
	alertEngine.Stop()
	sshCache.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		exitWithError(exitShutdownError, "Server forced to shutdown", err)
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
