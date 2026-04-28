package handlers

import (
	"net/http"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ServerHandler struct{}

func NewServerHandler() *ServerHandler { return &ServerHandler{} }

type CreateServerRequest struct {
	Name        string `json:"name" binding:"required"`
	Host        string `json:"host" binding:"required"`
	Port        int    `json:"port"`
	SSHUsername string `json:"ssh_username" binding:"required"`
	SSHPassword string `json:"ssh_password"`
	SSHKey      string `json:"ssh_key"`
}

type UpdateServerRequest struct {
	Name        string `json:"name" binding:"required"`
	Host        string `json:"host" binding:"required"`
	Port        int    `json:"port"`
	SSHUsername string `json:"ssh_username" binding:"required"`
	SSHPassword string `json:"ssh_password"`
	SSHKey      string `json:"ssh_key"`
}

func (h *ServerHandler) List(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	servers, err := models.GetServersByUserID(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load servers"})
		return
	}
	if servers == nil {
		servers = []models.Server{}
	}
	// attach tags
	for i := range servers {
		tags, _ := models.GetServerTags(db.Raw, servers[i].ID)
		if tags != nil {
			servers[i].Tags = tags
		}
	}
	c.JSON(http.StatusOK, servers)
}

func (h *ServerHandler) Create(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req CreateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}
	db := c.MustGet("db").(*models.DB)
	s := &models.Server{
		UserID:      userID,
		Name:        req.Name,
		Host:        req.Host,
		Port:        req.Port,
		SSHUsername: req.SSHUsername,
		SSHPassword: req.SSHPassword,
		SSHKey:      req.SSHKey,
	}
	if err := models.CreateServer(db, s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create server"})
		return
	}
	c.JSON(http.StatusCreated, s)
}

func (h *ServerHandler) Update(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req UpdateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}
	db := c.MustGet("db").(*models.DB)
	s := &models.Server{
		ID:          id,
		UserID:      userID,
		Name:        req.Name,
		Host:        req.Host,
		Port:        req.Port,
		SSHUsername: req.SSHUsername,
		SSHPassword: req.SSHPassword,
		SSHKey:      req.SSHKey,
	}
	if err := models.UpdateServer(db, s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update server"})
		return
	}
	c.JSON(http.StatusOK, s)
}

func (h *ServerHandler) Delete(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.DeleteServer(db.Raw, id, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete server"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}

func (h *ServerHandler) SetTags(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		TagIDs []uuid.UUID `json:"tag_ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.SetServerTags(db.Raw, id, userID, req.TagIDs); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to set tags"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "tags updated"})
}
