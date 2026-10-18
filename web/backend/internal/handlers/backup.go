package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"server-monitor/internal/crypto"
	"server-monitor/internal/models"
)

type BackupHandler struct{ slots chan struct{} }

func NewBackupHandler() *BackupHandler { return &BackupHandler{slots: make(chan struct{}, 2)} }

type backupRequest struct {
	Password           string `json:"password"`
	CurrentPassword    string `json:"current_password"`
	IncludeCredentials bool   `json:"include_credentials"`
	Data               string `json:"data"`
	Digest             string `json:"digest"`
}

func (h *BackupHandler) request(c *gin.Context) (backupRequest, *models.DB, bool) {
	var request backupRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 24<<20)
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(400, gin.H{"error": "invalid or oversized backup request"})
		return request, nil, false
	}
	if len(request.Password) < 12 || len(request.Password) > 1024 || len(request.CurrentPassword) > 1024 {
		c.JSON(400, gin.H{"error": "backup password must be 12–1024 bytes"})
		return request, nil, false
	}
	db := c.MustGet("db").(*models.DB)
	user, err := models.GetUserByID(db.Raw, c.MustGet("user_id").(uuid.UUID))
	if err != nil || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(request.CurrentPassword)) != nil {
		c.JSON(403, gin.H{"error": "current account password is incorrect"})
		return request, nil, false
	}
	return request, db, true
}
func (h *BackupHandler) acquire(c *gin.Context) bool {
	select {
	case h.slots <- struct{}{}:
		return true
	default:
		c.JSON(429, gin.H{"error": "another backup operation is running; retry shortly"})
		return false
	}
}
func (h *BackupHandler) Export(c *gin.Context) {
	request, db, ok := h.request(c)
	if !ok {
		return
	}
	if !h.acquire(c) {
		return
	}
	defer func() { <-h.slots }()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	backup, err := models.ExportConfiguration(ctx, db, c.MustGet("user_id").(uuid.UUID), request.IncludeCredentials)
	if err != nil {
		c.JSON(500, gin.H{"error": "could not read complete configuration; export canceled"})
		return
	}
	plain, err := json.Marshal(backup)
	if err != nil {
		c.JSON(500, gin.H{"error": "could not serialize configuration"})
		return
	}
	encrypted, err := crypto.SealBackup(plain, request.Password)
	for i := range plain {
		plain[i] = 0
	}
	if err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if _, err := db.Raw.ExecContext(ctx, `INSERT INTO configuration_audit(user_id,action,details) VALUES($1,'export',jsonb_build_object('backup_id',$2::text,'includes_credentials',$3::bool))`, c.MustGet("user_id"), backup.ID.String(), request.IncludeCredentials); err != nil {
		c.JSON(500, gin.H{"error": "could not record configuration export"})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="server-monitor-`+time.Now().UTC().Format("20060102-150405")+`.smbak"`)
	c.Data(200, "application/octet-stream", encrypted)
}
func (h *BackupHandler) decode(c *gin.Context, request backupRequest) (*models.ConfigurationBackup, string, bool) {
	data, err := base64.StdEncoding.DecodeString(request.Data)
	if err != nil || len(data) > crypto.MaxBackupSize+128 {
		c.JSON(400, gin.H{"error": "invalid backup file"})
		return nil, "", false
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	plain, err := crypto.OpenBackup(data, request.Password)
	if err != nil {
		c.JSON(400, gin.H{"error": crypto.ErrBackupPassword.Error()})
		return nil, "", false
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	var backup models.ConfigurationBackup
	if err := json.Unmarshal(plain, &backup); err != nil {
		c.JSON(400, gin.H{"error": "invalid configuration backup"})
		return nil, "", false
	}
	if err := models.ValidateConfigurationBackup(&backup); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return nil, "", false
	}
	return &backup, digest, true
}
func (h *BackupHandler) Preview(c *gin.Context) {
	request, db, ok := h.request(c)
	if !ok {
		return
	}
	if !h.acquire(c) {
		return
	}
	defer func() { <-h.slots }()
	backup, digest, ok := h.decode(c, request)
	if !ok {
		return
	}
	preview := backup.Preview()
	var err error
	preview.AlreadyImported, err = models.ConfigurationImported(c.Request.Context(), db.Raw, c.MustGet("user_id").(uuid.UUID), backup.ID)
	if err != nil {
		c.JSON(500, gin.H{"error": "cannot verify previous imports"})
		return
	}
	c.JSON(200, gin.H{"preview": preview, "digest": digest, "activation": "paused", "visibility": "private", "tag_conflicts": "reuse_existing_names"})
}
func (h *BackupHandler) Import(c *gin.Context) {
	request, db, ok := h.request(c)
	if !ok {
		return
	}
	if !h.acquire(c) {
		return
	}
	defer func() { <-h.slots }()
	backup, digest, ok := h.decode(c, request)
	if !ok {
		return
	}
	if request.Digest != digest {
		c.JSON(409, gin.H{"error": "preview this exact backup before restoring"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 45*time.Second)
	defer cancel()
	preview, err := models.ImportConfiguration(ctx, db, c.MustGet("user_id").(uuid.UUID), backup)
	if errors.Is(err, models.ErrBackupImported) {
		c.JSON(409, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		c.JSON(400, gin.H{"error": "restore failed; no configuration was changed"})
		return
	}
	c.JSON(201, gin.H{"preview": preview, "activation": "paused", "visibility": "private"})
}
