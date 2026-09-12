package handlers

import (
	"context"
	"errors"
	"hash/fnv"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"server-monitor/internal/models"
	"server-monitor/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type FileHandler struct {
	sshCache *services.SSHConnCache
	slots    chan struct{}
	writes   [64]sync.Mutex
}

func NewFileHandler(cache *services.SSHConnCache) *FileHandler {
	return &FileHandler{sshCache: cache, slots: make(chan struct{}, 8)}
}

func (handler *FileHandler) Deadline(httpContext *gin.Context) {
	ctx, cancel := context.WithTimeout(httpContext.Request.Context(), services.FileOperationTimeout)
	defer cancel()
	httpContext.Request = httpContext.Request.WithContext(ctx)
	controller := http.NewResponseController(httpContext.Writer)
	_ = controller.SetReadDeadline(time.Now().Add(services.FileOperationTimeout))
	_ = controller.SetWriteDeadline(time.Now().Add(services.FileOperationTimeout + 30*time.Second))
	defer controller.SetReadDeadline(time.Time{})
	defer controller.SetWriteDeadline(time.Time{})
	httpContext.Next()
}

func (handler *FileHandler) connect(httpContext *gin.Context) (services.RemoteFiles, func(), bool) {
	serverID, err := uuid.Parse(httpContext.Param("id"))
	containerID := httpContext.Query("container")
	if err != nil || (containerID != "" && !services.ValidFileContainerID(containerID)) {
		fileError(httpContext, services.ErrFilePath)
		return nil, nil, false
	}
	server, err := models.GetServerByIDAndUser(httpContext.MustGet("db").(*models.DB), serverID, httpContext.MustGet("user_id").(uuid.UUID))
	if err != nil {
		httpContext.JSON(http.StatusNotFound, gin.H{"error": "server not found", "code": "server_not_found"})
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(httpContext.Request.Context(), services.FileOperationTimeout)
	select {
	case handler.slots <- struct{}{}:
	case <-ctx.Done():
		cancel()
		fileError(httpContext, ctx.Err())
		return nil, nil, false
	}
	release := func() { cancel(); <-handler.slots }
	client, err := handler.sshCache.Get(server)
	if err != nil {
		release()
		fileError(httpContext, err)
		return nil, nil, false
	}
	var store services.RemoteFiles
	if containerID == "" {
		store, err = services.NewSFTPFiles(ctx, client, server.ServerType)
	} else {
		store, err = services.NewContainerFiles(ctx, client, containerID, server.ServerType)
	}
	if err != nil {
		release()
		fileError(httpContext, err)
		return nil, nil, false
	}
	return store, func() { _ = store.Close(); release() }, true
}

func filePath(httpContext *gin.Context, allowHome bool) (string, bool) {
	filename, err := services.CleanRemotePath(httpContext.Query("path"), allowHome)
	if err != nil {
		fileError(httpContext, err)
		return "", false
	}
	return filename, true
}

func fileError(httpContext *gin.Context, err error) {
	status, code, message := http.StatusBadGateway, "remote_failed", "File operation failed; check SSH/SFTP access, permissions and container tools"
	switch {
	case errors.Is(err, services.ErrFilePath):
		status, code, message = http.StatusBadRequest, "invalid_path", err.Error()
	case errors.Is(err, services.ErrFileTooLarge):
		status, code, message = http.StatusRequestEntityTooLarge, "file_too_large", err.Error()
	case errors.Is(err, services.ErrFileNotText):
		status, code, message = http.StatusUnsupportedMediaType, "not_text", err.Error()
	case errors.Is(err, services.ErrFileExists):
		status, code, message = http.StatusConflict, "file_exists", err.Error()
	case errors.Is(err, services.ErrFileChanged):
		status, code, message = http.StatusConflict, "file_changed", err.Error()
	case errors.Is(err, services.ErrFileNotRegular):
		status, code, message = http.StatusBadRequest, "not_regular", err.Error()
	case errors.Is(err, services.ErrFilesUnsupported):
		status, code, message = http.StatusUnprocessableEntity, "unsupported", err.Error()
	case os.IsNotExist(err):
		status, code, message = http.StatusNotFound, "not_found", "File or directory not found"
	case os.IsPermission(err):
		status, code, message = http.StatusForbidden, "permission_denied", "Remote file permission denied"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled), os.IsTimeout(err):
		status, code, message = http.StatusGatewayTimeout, "timeout", "File operation timed out or was canceled"
	}
	if status == http.StatusBadGateway {
		log.Printf("file operation server=%s: %v", httpContext.Param("id"), err)
	}
	httpContext.Set("file_error", code)
	httpContext.JSON(status, gin.H{"error": message, "code": code})
}

func (handler *FileHandler) List(httpContext *gin.Context) {
	directory, ok := filePath(httpContext, true)
	if !ok {
		return
	}
	offset, parseErr := strconv.Atoi(httpContext.DefaultQuery("cursor", "0"))
	limit, limitErr := strconv.Atoi(httpContext.DefaultQuery("limit", "100"))
	if parseErr != nil || limitErr != nil || offset < 0 || offset > 1000000 || limit < 1 || limit > 200 {
		fileError(httpContext, services.ErrFilePath)
		return
	}
	store, closeStore, ok := handler.managed(httpContext)
	if !ok {
		return
	}
	defer closeStore()
	listing, err := store.Page(directory, offset, limit)
	if err != nil {
		fileError(httpContext, err)
		return
	}
	httpContext.JSON(http.StatusOK, listing)
}

func (handler *FileHandler) ReadText(httpContext *gin.Context) {
	filename, ok := filePath(httpContext, false)
	if !ok {
		return
	}
	store, closeStore, ok := handler.connect(httpContext)
	if !ok {
		return
	}
	defer closeStore()
	content, err := services.ReadRemoteText(store, filename)
	if err != nil {
		fileError(httpContext, err)
		return
	}
	httpContext.JSON(http.StatusOK, gin.H{"path": filename, "content": string(content), "revision": services.FileRevision(content)})
}

func (handler *FileHandler) lock(httpContext *gin.Context, filename string) func() {
	digest := fnv.New32a()
	_, _ = io.WriteString(digest, httpContext.Param("id")+":"+httpContext.Query("container")+":"+filename)
	mutex := &handler.writes[digest.Sum32()%uint32(len(handler.writes))]
	mutex.Lock()
	return mutex.Unlock
}

func (handler *FileHandler) SaveText(httpContext *gin.Context) {
	filename, ok := filePath(httpContext, false)
	if !ok {
		return
	}
	httpContext.Request.Body = http.MaxBytesReader(httpContext.Writer, httpContext.Request.Body, services.MaxTextFileSize*6+4096)
	var request struct {
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	if err := httpContext.ShouldBindJSON(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fileError(httpContext, services.ErrFileTooLarge)
		} else {
			httpContext.JSON(http.StatusBadRequest, gin.H{"error": "invalid text request", "code": "invalid_request"})
		}
		return
	}
	if len(request.Content) > services.MaxTextFileSize {
		fileError(httpContext, services.ErrFileTooLarge)
		return
	}
	if len(request.Revision) != 64 {
		fileError(httpContext, services.ErrFileChanged)
		return
	}
	store, closeStore, ok := handler.connect(httpContext)
	if !ok {
		return
	}
	defer closeStore()
	unlock := handler.lock(httpContext, filename)
	defer unlock()
	current, err := services.ReadRemoteText(store, filename)
	if err != nil {
		fileError(httpContext, err)
		return
	}
	if services.FileRevision(current) != request.Revision {
		fileError(httpContext, services.ErrFileChanged)
		return
	}
	backup, err := services.BackupRemoteText(store, filename, current)
	if err != nil {
		fileError(httpContext, err)
		return
	}
	httpContext.Set("file_backup", backup)
	if err := services.SaveRemoteText(store, filename, request.Content, request.Revision); err != nil {
		fileError(httpContext, err)
		return
	}
	httpContext.JSON(http.StatusOK, gin.H{"revision": services.FileRevision([]byte(request.Content)), "backup_path": backup})
}

func (handler *FileHandler) Download(httpContext *gin.Context) {
	filename, ok := filePath(httpContext, false)
	if !ok {
		return
	}
	store, closeStore, ok := handler.connect(httpContext)
	if !ok {
		return
	}
	defer closeStore()
	temporary, err := os.CreateTemp("", "server-monitor-download-*")
	if err != nil {
		fileError(httpContext, err)
		return
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if err := store.Read(filename, temporary, services.MaxDownloadFileSize); err != nil {
		fileError(httpContext, err)
		return
	}
	info, err := temporary.Stat()
	if err != nil {
		fileError(httpContext, err)
		return
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		fileError(httpContext, err)
		return
	}
	name := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, path.Base(filename))
	httpContext.Header("X-Content-Type-Options", "nosniff")
	httpContext.DataFromReader(http.StatusOK, info.Size(), "application/octet-stream", temporary, map[string]string{"Content-Disposition": mime.FormatMediaType("attachment", map[string]string{"filename": name})})
}

func (handler *FileHandler) Upload(httpContext *gin.Context) {
	directory, ok := filePath(httpContext, false)
	if !ok {
		return
	}
	store, closeStore, ok := handler.connect(httpContext)
	if !ok {
		return
	}
	defer closeStore()
	httpContext.Request.Body = http.MaxBytesReader(httpContext.Writer, httpContext.Request.Body, services.MaxUploadFileSize+(1<<20))
	multipart, err := httpContext.Request.MultipartReader()
	if err != nil {
		httpContext.JSON(http.StatusBadRequest, gin.H{"error": "multipart file required", "code": "invalid_request"})
		return
	}
	part, err := multipart.NextPart()
	if err != nil {
		httpContext.JSON(http.StatusBadRequest, gin.H{"error": "file required", "code": "invalid_request"})
		return
	}
	defer part.Close()
	_, parameters, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	name := parameters["filename"]
	if err != nil || part.FormName() != "file" || !services.ValidUploadName(name) {
		fileError(httpContext, services.ErrFilePath)
		return
	}
	temporary, err := os.CreateTemp("", "server-monitor-upload-*")
	if err != nil {
		fileError(httpContext, err)
		return
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	size, err := io.Copy(temporary, io.LimitReader(part, services.MaxUploadFileSize+1))
	var tooLarge *http.MaxBytesError
	if size > services.MaxUploadFileSize || errors.As(err, &tooLarge) {
		fileError(httpContext, services.ErrFileTooLarge)
		return
	}
	if err != nil {
		fileError(httpContext, err)
		return
	}
	if _, err := multipart.NextPart(); err != io.EOF {
		httpContext.JSON(http.StatusBadRequest, gin.H{"error": "send one file per request", "code": "invalid_request"})
		return
	}
	if _, err := temporary.Seek(0, io.SeekStart); err != nil {
		fileError(httpContext, err)
		return
	}
	filename := path.Join(directory, name)
	httpContext.Set("file_target", filename)
	unlock := handler.lock(httpContext, filename)
	defer unlock()
	version := httpContext.Query("version")
	if version == "" {
		fileError(httpContext, services.ErrFileChanged)
		return
	}
	if err := expectedUpload(store.(services.ManagedFiles), filename, version); err != nil {
		fileError(httpContext, err)
		return
	}
	if err := store.Write(filename, temporary, size, httpContext.Query("overwrite") == "1"); err != nil {
		fileError(httpContext, err)
		return
	}
	httpContext.JSON(http.StatusOK, gin.H{"path": filename, "size": size})
}
