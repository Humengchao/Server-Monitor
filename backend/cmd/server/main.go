package main

import (
	"log"
	"time"

	"server-monitor/internal/config"
	"server-monitor/internal/database"
	"server-monitor/internal/router"
	"server-monitor/internal/services"
)

func main() {
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
	collector := services.NewCollector(db, time.Duration(cfg.PollInterval)*time.Second)
	collector.Start()

	log.Printf("Server starting on :%s", cfg.ServerPort)
	r := router.Setup(db, cfg)
	if err := r.Run(":" + cfg.ServerPort); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
