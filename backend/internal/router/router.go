package router

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"server-monitor/internal/config"
	"server-monitor/internal/handlers"
	"server-monitor/internal/middleware"
	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
)

func Setup(db *sql.DB, cfg *config.Config, sshCache *services.SSHConnCache, notifier *services.WebhookNotifier, collector *services.Collector) (*gin.Engine, error) {
	r := gin.New()
	// Only honor X-Forwarded-For from the configured reverse proxies; gin's
	// default trusts every client, which would let direct requests spoof their
	// IP for rate limiting and login history.
	if err := r.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		return nil, fmt.Errorf("set trusted proxies: %w", err)
	}
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		SkipPaths: []string{"/api/health", "/api/public/status"},
		// WebSocket URLs may still carry a legacy auth token in their query;
		// keep them out of the access log entirely.
		Skip: func(c *gin.Context) bool {
			path := c.Request.URL.Path
			return strings.HasPrefix(path, "/api/ssh/") || strings.HasPrefix(path, "/api/ws/")
		},
	}), gin.Recovery())

	// CORS
	r.Use(func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == cfg.CORSOrigin || cfg.CORSOrigin == "*" {
			c.Header("Access-Control-Allow-Origin", origin)
		}
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		// The API client sends Cache-Control and Pragma on every request; if
		// they are missing here, a cross-origin deployment fails preflight on
		// every call. Same-origin setups behind nginx never send a preflight,
		// which is why this only bites when CORS_ORIGIN is actually used.
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,Cache-Control,Pragma")
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
	serverH := handlers.NewServerHandler(sshCache, collector)
	tagH := handlers.NewTagHandler()
	metricsH := handlers.NewMetricsHandler()
	sshH := handlers.NewSSHHandler()
	dockerH := handlers.NewDockerHandler(sshCache)
	processH := handlers.NewProcessHandler(sshCache)
	hostOpsH := handlers.NewHostOpsHandler(sshCache)
	batchH := handlers.NewBatchHandler(sshCache)
	uptimeH := handlers.NewUptimeHandler()
	credH := handlers.NewCredentialHandler()
	publicStatusH := handlers.NewPublicStatusHandler()
	alertH := handlers.NewAlertHandler(notifier)
	settingsH := handlers.NewSettingsHandler(notifier)

	rateLimit := middleware.RateLimit(5, 1*time.Minute)

	// Health check endpoint
	r.GET("/api/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "ok"})
	})

	// Public, anonymized status endpoint. It is rate-limited separately and its
	// model query never selects connection details or ownership information.
	r.GET("/api/public/status", middleware.RateLimit(60, 1*time.Minute), publicStatusH.Get)

	api := r.Group("/api")
	{
		auth := api.Group("/auth")
		{
			auth.POST("/register", rateLimit, authH.Register)
			auth.POST("/login", rateLimit, authH.Login)
			auth.GET("/me", middleware.AuthRequired(cfg), authH.Me)
			auth.GET("/login-history", middleware.AuthRequired(cfg), authH.LoginHistory)
			// Rate limited because it verifies the current password, but on its
			// own bucket: sharing login's would mean a few mistyped current
			// passwords also lock the user out of signing in.
			auth.POST("/password", middleware.AuthRequired(cfg),
				middleware.RateLimit(10, 1*time.Minute), authH.ChangePassword)
		}

		servers := api.Group("/servers", middleware.AuthRequired(cfg))
		{
			servers.GET("", serverH.List)
			// Registered before "/:id" so gin routes the literal segment rather
			// than treating "bulk" as a server ID.
			servers.POST("/bulk/tags", batchH.BulkTags)
			servers.POST("/bulk/delete", batchH.BulkDelete)
			// Fanning a command out over SSH is the heaviest thing the panel
			// does; limit how often it can be triggered.
			servers.POST("/bulk/exec", middleware.RateLimit(10, 1*time.Minute), batchH.BulkExec)
			servers.GET("/uptime", uptimeH.Fleet)
			servers.GET("/:id", serverH.Get)
			servers.GET("/:id/uptime", uptimeH.Detail)
			servers.POST("", serverH.Create)
			servers.PUT("/:id", serverH.Update)
			servers.DELETE("/:id", serverH.Delete)
			servers.PUT("/:id/tags", serverH.SetTags)
			// Reaches out over SSH on demand, so it gets its own bucket rather
			// than sharing the read-only routes'.
			servers.POST("/:id/poll", middleware.RateLimit(20, 1*time.Minute), serverH.PollRetry)
			servers.GET("/:id/metrics/latest", metricsH.GetLatest)
			servers.GET("/:id/metrics", metricsH.GetHistory)
			servers.GET("/:id/processes", processH.List)
			servers.DELETE("/:id/processes/:pid", processH.Kill)
			servers.GET("/:id/services", hostOpsH.ListServices)
			// Starting and stopping units is a privileged, side-effecting action;
			// it gets its own bucket rather than sharing the read-only routes'.
			servers.POST("/:id/services/control",
				middleware.RateLimit(30, 1*time.Minute), hostOpsH.ControlService)
			servers.GET("/:id/ports", hostOpsH.ListPorts)
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

		settings := api.Group("/settings", middleware.AuthRequired(cfg))
		{
			settings.GET("", settingsH.Get)
			settings.PUT("", settingsH.Update)
		}

		tags := api.Group("/tags", middleware.AuthRequired(cfg))
		{
			tags.GET("", tagH.List)
			tags.POST("", tagH.Create)
			tags.PUT("/:id", tagH.Update)
			tags.DELETE("/:id", tagH.Delete)
		}

		credentials := api.Group("/credentials", middleware.AuthRequired(cfg))
		{
			credentials.GET("", credH.List)
			credentials.POST("", credH.Create)
			credentials.PUT("/:id", credH.Update)
			credentials.DELETE("/:id", credH.Delete)
		}

		alerts := api.Group("/alerts", middleware.AuthRequired(cfg))
		{
			alerts.GET("/rules", alertH.ListRules)
			alerts.POST("/rules", alertH.CreateRule)
			alerts.PUT("/rules/:id", alertH.UpdateRule)
			alerts.DELETE("/rules/:id", alertH.DeleteRule)
			alerts.GET("/events", alertH.ListEvents)
			alerts.GET("/summary", alertH.Summary)
			// Outbound probe: rate-limited so the endpoint can't be used to
			// hammer a third-party host through the panel.
			alerts.POST("/test", middleware.RateLimit(6, 1*time.Minute), alertH.TestWebhook)
		}

		api.GET("/ssh/:id", middleware.WSAuthRequired(cfg), sshH.Handle)
	}

	return r, nil
}
