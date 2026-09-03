package handlers

import (
	"errors"
	"net/http"
	"strings"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type TagHandler struct{}

func NewTagHandler() *TagHandler { return &TagHandler{} }

func (h *TagHandler) List(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	tags, err := models.GetTagsByUserID(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load tags"})
		return
	}
	if tags == nil {
		tags = []models.Tag{}
	}
	c.JSON(http.StatusOK, tags)
}

// tagRequest is shared by create and update. The bounds match the column types
// (name VARCHAR(64), color VARCHAR(7)); without them an over-long value reached
// Postgres and came back as a 500 rather than a message the user can act on.
type tagRequest struct {
	Name  string `json:"name" binding:"required,max=64"`
	Color string `json:"color" binding:"max=7"`
}

func (h *TagHandler) Create(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req tagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.Color == "" {
		req.Color = "#1890ff"
	}
	t := &models.Tag{UserID: userID, Name: req.Name, Color: req.Color}
	db := c.MustGet("db").(*models.DB)
	if err := models.CreateTag(db.Raw, t); err != nil {
		if errors.Is(err, models.ErrTagNameTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": "tag name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create tag"})
		return
	}
	c.JSON(http.StatusCreated, t)
}

// Update renames or recolours an existing tag. Deleting and recreating was the
// only way to do this before, and that cascades through server_tags — every
// server carrying the tag silently lost it.
func (h *TagHandler) Update(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req tagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if req.Color == "" {
		req.Color = "#1890ff"
	}
	t := &models.Tag{ID: id, UserID: userID, Name: req.Name, Color: req.Color}
	db := c.MustGet("db").(*models.DB)
	found, err := models.UpdateTag(db.Raw, t)
	if err != nil {
		if errors.Is(err, models.ErrTagNameTaken) {
			c.JSON(http.StatusConflict, gin.H{"error": "tag name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update tag"})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "tag not found"})
		return
	}
	c.JSON(http.StatusOK, t)
}

func (h *TagHandler) Delete(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.DeleteTag(db.Raw, id, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete tag"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
