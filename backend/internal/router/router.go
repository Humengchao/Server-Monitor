package router

import (
	"database/sql"

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
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	// Inject DB
	dbWrapper := &models.DB{Raw: db}
	r.Use(func(c *gin.Context) {
		c.Set("db", dbWrapper)
		c.Next()
	})

	authH := handlers.NewAuthHandler(cfg)
	serverH := handlers.NewServerHandler()
	tagH := handlers.NewTagHandler()
	metricsH := handlers.NewMetricsHandler()
	sshH := handlers.NewSSHHandler()

	api := r.Group("/api")
	{
		auth := api.Group("/auth")
		{
			auth.POST("/register", authH.Register)
			auth.POST("/login", authH.Login)
			auth.GET("/me", middleware.AuthRequired(cfg), authH.Me)
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
		}

		tags := api.Group("/tags", middleware.AuthRequired(cfg))
		{
			tags.GET("", tagH.List)
			tags.POST("", tagH.Create)
			tags.DELETE("/:id", tagH.Delete)
		}

		api.GET("/ssh/:id", middleware.AuthRequired(cfg), sshH.Handle)
	}

	return r
}
