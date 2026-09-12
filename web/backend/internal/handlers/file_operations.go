package handlers

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"log"
	"net/http"
	"os"
	"path"
	"server-monitor/internal/models"
	"server-monitor/internal/services"
	"strings"
	"time"
)

func (handler *FileHandler) managed(httpContext *gin.Context) (services.ManagedFiles, func(), bool) {
	store, closeStore, ok := handler.connect(httpContext)
	if !ok {
		return nil, nil, false
	}
	managed, ok := store.(services.ManagedFiles)
	if !ok {
		closeStore()
		fileError(httpContext, services.ErrFilesUnsupported)
		return nil, nil, false
	}
	return managed, closeStore, true
}
func (handler *FileHandler) Metadata(httpContext *gin.Context) {
	filename, ok := filePath(httpContext, false)
	if !ok {
		return
	}
	store, closeStore, ok := handler.managed(httpContext)
	if !ok {
		return
	}
	defer closeStore()
	entry, err := store.Metadata(filename)
	if os.IsNotExist(err) {
		httpContext.JSON(200, gin.H{"exists": false, "version": "missing"})
		return
	}
	if err != nil {
		fileError(httpContext, err)
		return
	}
	httpContext.JSON(200, gin.H{"exists": true, "entry": entry, "version": services.FileMetadataVersion(entry)})
}
func safeMutationPath(filename string) bool {
	return path.Base(filename) != "." && filename != servicesRemoteParent(filename)
}
func servicesRemoteParent(filename string) string {
	parent := path.Dir(filename)
	if len(filename) == 3 && filename[1:] == ":/" {
		return filename
	}
	if len(filename) == 4 && filename[0] == '/' && filename[2:] == ":/" {
		return filename
	}
	return parent
}
func (handler *FileHandler) Change(httpContext *gin.Context) {
	filename, ok := filePath(httpContext, false)
	if !ok {
		return
	}
	if !safeMutationPath(filename) {
		fileError(httpContext, services.ErrFilePath)
		return
	}
	httpContext.Request.Body = http.MaxBytesReader(httpContext.Writer, httpContext.Request.Body, 8192)
	var request struct {
		Action  string `json:"action"`
		Name    string `json:"name"`
		Kind    string `json:"kind"`
		Version string `json:"version"`
	}
	if err := httpContext.ShouldBindJSON(&request); err != nil {
		fileError(httpContext, services.ErrFilePath)
		return
	}
	if request.Action != "create" && request.Action != "rename" && request.Action != "delete" {
		fileError(httpContext, services.ErrFilePath)
		return
	}
	if request.Action == "create" && request.Kind != "file" && request.Kind != "directory" {
		fileError(httpContext, services.ErrFilePath)
		return
	}
	httpContext.Set("file_action", request.Action)
	store, closeStore, ok := handler.managed(httpContext)
	if !ok {
		return
	}
	defer closeStore()
	unlock := handler.lock(httpContext, filename)
	defer unlock()
	var err error
	destination := ""
	if request.Action != "create" {
		entry, statErr := store.Metadata(filename)
		if statErr != nil {
			fileError(httpContext, statErr)
			return
		}
		if request.Version == "" || services.FileMetadataVersion(entry) != request.Version {
			fileError(httpContext, services.ErrFileChanged)
			return
		}
	}
	switch request.Action {
	case "create":
		if request.Kind == "directory" {
			err = store.Mkdir(filename, false)
		} else {
			err = store.Write(filename, strings.NewReader(""), 0, false)
		}
	case "rename":
		if !services.ValidUploadName(request.Name) {
			fileError(httpContext, services.ErrFilePath)
			return
		}
		destination = path.Join(path.Dir(filename), request.Name)
		httpContext.Set("file_target", destination)
		err = store.Rename(filename, destination)
	case "delete":
		err = store.Remove(filename)
	}
	if err != nil {
		fileError(httpContext, err)
		return
	}
	httpContext.JSON(200, gin.H{"path": filename, "target": destination})
}
func (handler *FileHandler) Audit(httpContext *gin.Context) {
	id, err := uuid.Parse(httpContext.Param("id"))
	if err != nil {
		httpContext.AbortWithStatus(400)
		return
	}
	user := httpContext.MustGet("user_id").(uuid.UUID)
	database := httpContext.MustGet("db").(*models.DB).Raw
	owned, err := models.ServerOwnedByUser(database, id, user)
	if err != nil || !owned {
		httpContext.AbortWithStatus(404)
		return
	}
	auditID := uuid.New()
	action := httpContext.Request.Method + " " + path.Base(httpContext.Request.URL.Path)
	_, err = database.ExecContext(httpContext.Request.Context(), "INSERT INTO file_operations(id,user_id,server_id,container_id,action,path,outcome) VALUES($1,$2,$3,$4,$5,$6,'pending')", auditID, user, id, httpContext.Query("container"), action, httpContext.Query("path"))
	if err != nil {
		httpContext.AbortWithStatusJSON(503, gin.H{"code": "audit_unavailable", "error": "Audit storage unavailable; operation canceled"})
		return
	}
	httpContext.Next()
	outcome := "success"
	if httpContext.Writer.Status() >= 400 {
		outcome = "failed"
	}
	if value, ok := httpContext.Get("file_action"); ok {
		action, _ = value.(string)
	}
	completion, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = database.ExecContext(completion, "UPDATE file_operations SET action=$2,target=$3,backup_path=$4,outcome=$5,error_code=$6 WHERE id=$1", auditID, action, httpContext.GetString("file_target"), httpContext.GetString("file_backup"), outcome, httpContext.GetString("file_error"))
	if err != nil {
		log.Printf("file audit completion failed id=%s: %v", auditID, err)
	}
}
func (handler *FileHandler) History(httpContext *gin.Context) {
	id, err := uuid.Parse(httpContext.Param("id"))
	if err != nil {
		httpContext.Status(400)
		return
	}
	user := httpContext.MustGet("user_id").(uuid.UUID)
	database := httpContext.MustGet("db").(*models.DB).Raw
	owned, err := models.ServerOwnedByUser(database, id, user)
	if err != nil || !owned {
		httpContext.Status(404)
		return
	}
	rows, err := database.QueryContext(httpContext.Request.Context(), "SELECT id,action,path,target,backup_path,outcome,error_code,created_at FROM file_operations WHERE user_id=$1 AND server_id=$2 AND container_id=$3 ORDER BY created_at DESC LIMIT 100", user, id, httpContext.Query("container"))
	if err != nil {
		httpContext.Status(500)
		return
	}
	defer rows.Close()
	entries := []gin.H{}
	for rows.Next() {
		var key uuid.UUID
		var action, filename, target, backup, outcome, code string
		var created time.Time
		if err = rows.Scan(&key, &action, &filename, &target, &backup, &outcome, &code, &created); err != nil {
			httpContext.Status(500)
			return
		}
		entries = append(entries, gin.H{"id": key, "action": action, "path": filename, "target": target, "backup_path": backup, "outcome": outcome, "error_code": code, "created_at": created})
	}
	if rows.Err() != nil {
		httpContext.Status(500)
		return
	}
	httpContext.JSON(200, entries)
}
func expectedUpload(store services.ManagedFiles, filename, version string) error {
	entry, err := store.Metadata(filename)
	if os.IsNotExist(err) {
		if version == "missing" {
			return nil
		}
		return services.ErrFileChanged
	}
	if err != nil {
		return err
	}
	if version == "missing" {
		return services.ErrFileExists
	}
	if services.FileMetadataVersion(entry) != version {
		return services.ErrFileChanged
	}
	if !entry.IsFile || entry.IsSymlink {
		return services.ErrFileNotRegular
	}
	return nil
}
