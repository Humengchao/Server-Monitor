package handlers

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"server-monitor/internal/models"
)

func (h *ServerHandler) UpdateNotes(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	var request struct {
		Notes *string `json:"notes" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "notes must be a string"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	err = models.UpdateServerNotes(c.Request.Context(), db.Raw, id, c.MustGet("user_id").(uuid.UUID), *request.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "server not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update notes"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}
