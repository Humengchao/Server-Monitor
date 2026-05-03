package router

import (
	"database/sql"
	"time"

	"server-monitor/internal/config"
	"server-monitor/internal/handlers"
	"server-monitor/internal/middleware"
	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
)

func Setup(db *sql.DB, cfg *config.Config) *gin.Engine {
	r := gin.Default()

	// CORS
	r.Use(func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == cfg.CORSOrigin || cfg.CORSOrigin == "*" {
			c.Header("Access-Control-Allow-Origin", origin)
		}
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// No-cache for API responses
	r.Use(func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Next()
	})

	// Inject DB
	dbWrapper := &models.DB{Raw: db, EncryptionKey: cfg.EncryptionKey}
	r.Use(func(c *gin.Context) {
		c.Set("db", dbWrapper)
		c.Next()
	})

	authH := handlers.NewAuthHandler(cfg)
	serverH := handlers.NewServerHandler()
	tagH := handlers.NewTagHandler()
	metricsH := handlers.NewMetricsHandler()
	sshH := handlers.NewSSHHandler()
	dockerH := handlers.NewDockerHandler()

	rateLimit := middleware.RateLimit(5, 1*time.Minute)

	api := r.Group("/api")
	{
		auth := api.Group("/auth")
		{
			auth.POST("/register", rateLimit, authH.Register)
			auth.POST("/login", rateLimit, authH.Login)
			auth.GET("/me", middleware.AuthRequired(cfg), authH.Me)
			auth.GET("/login-history", middleware.AuthRequired(cfg), authH.LoginHistory)
		}

		servers := api.Group("/servers", middleware.AuthRequired(cfg))
		{
			servers.GET("", serverH.List)
			servers.POST("", serverH.Create)
			servers.PUT("/:id", serverH.Update)
			servers.DELETE("/:id", serverH.Delete)
			servers.PUT("/:id/tags", serverH.SetTags)
			servers.GET("/:id/metrics/latest", metricsH.GetLatest)
			servers.GET("/:id/metrics", metricsH.GetHistory)
			servers.GET("/:id/docker/check", dockerH.CheckDocker)
			servers.GET("/:id/docker/containers", dockerH.ListContainers)
			servers.POST("/:id/docker/containers/:containerId/:action", dockerH.ContainerAction)
			servers.GET("/:id/docker/containers/:containerId/logs", dockerH.ContainerLogs)
		}

		// WebSocket endpoints (token in query param)
		ws := api.Group("/ws", middleware.WSAuthRequired(cfg))
		{
			ws.GET("/servers/:id/docker/containers/:containerId/exec", dockerH.ContainerExec)
		}

		tags := api.Group("/tags", middleware.AuthRequired(cfg))
		{
			tags.GET("", tagH.List)
			tags.POST("", tagH.Create)
			tags.DELETE("/:id", tagH.Delete)
		}

		api.GET("/ssh/:id", middleware.WSAuthRequired(cfg), sshH.Handle)
	}

	return r
}
