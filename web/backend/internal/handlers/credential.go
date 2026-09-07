package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"server-monitor/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type CredentialHandler struct{}

func NewCredentialHandler() *CredentialHandler { return &CredentialHandler{} }

type CreateCredentialRequest struct {
	Name           string `json:"name" binding:"required"`
	SSHUsername    string `json:"ssh_username" binding:"required"`
	SSHPassword    string `json:"ssh_password"`
	SSHKey         string `json:"ssh_key"`
	CredentialType string `json:"credential_type"`
}

type UpdateCredentialRequest struct {
	Name           string `json:"name" binding:"required"`
	SSHUsername    string `json:"ssh_username" binding:"required"`
	SSHPassword    string `json:"ssh_password"`
	SSHKey         string `json:"ssh_key"`
	CredentialType string `json:"credential_type"`
}

func normalizeCredentialFields(name, username, credentialType string) (string, string, string, error) {
	name = strings.TrimSpace(name)
	username = strings.TrimSpace(username)
	credentialType = strings.ToLower(strings.TrimSpace(credentialType))
	if name == "" {
		return "", "", "", fmt.Errorf("credential name is required")
	}
	if username == "" {
		return "", "", "", fmt.Errorf("SSH username is required")
	}
	if strings.ContainsFunc(username, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.Is(unicode.C, r)
	}) {
		return "", "", "", fmt.Errorf("SSH username contains whitespace or control characters")
	}
	if credentialType == "" {
		credentialType = "linux"
	}
	if credentialType != "linux" && credentialType != "windows" {
		return "", "", "", fmt.Errorf("credential type must be linux or windows")
	}
	return name, username, credentialType, nil
}

func (h *CredentialHandler) List(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	db := c.MustGet("db").(*models.DB)
	creds, err := models.GetCredentialsByUserID(db.Raw, userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load credentials"})
		return
	}
	if creds == nil {
		creds = []models.Credential{}
	}
	c.JSON(http.StatusOK, creds)
}

func (h *CredentialHandler) Create(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	var req CreateCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	db := c.MustGet("db").(*models.DB)
	name, username, credentialType, err := normalizeCredentialFields(req.Name, req.SSHUsername, req.CredentialType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if req.SSHPassword == "" && strings.TrimSpace(req.SSHKey) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "SSH password or key is required"})
		return
	}
	req.Name, req.SSHUsername, req.CredentialType = name, username, credentialType
	cred := &models.Credential{
		UserID:      userID,
		Name:        req.Name,
		SSHUsername: req.SSHUsername,
		SSHPassword: req.SSHPassword,
		SSHKey:      req.SSHKey,
		CredType:    req.CredentialType,
	}
	if err := models.CreateCredential(db, cred); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create credential"})
		return
	}
	c.JSON(http.StatusCreated, cred)
}

func (h *CredentialHandler) Update(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req UpdateCredentialRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name, username, credentialType, err := normalizeCredentialFields(req.Name, req.SSHUsername, req.CredentialType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	req.Name, req.SSHUsername, req.CredentialType = name, username, credentialType
	db := c.MustGet("db").(*models.DB)
	cred := &models.Credential{
		ID:          id,
		UserID:      userID,
		Name:        req.Name,
		SSHUsername: req.SSHUsername,
		SSHPassword: req.SSHPassword,
		SSHKey:      req.SSHKey,
		CredType:    req.CredentialType,
	}
	if err := models.UpdateCredential(db, cred); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "credential not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update credential"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "updated"})
}

func (h *CredentialHandler) Delete(c *gin.Context) {
	userID := c.MustGet("user_id").(uuid.UUID)
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	db := c.MustGet("db").(*models.DB)
	if err := models.DeleteCredential(db.Raw, id, userID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete credential"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "deleted"})
}
