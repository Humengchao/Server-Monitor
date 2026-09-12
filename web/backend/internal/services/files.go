package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	MaxTextFileSize      = 2 << 20
	MaxUploadFileSize    = 64 << 20
	MaxDownloadFileSize  = 256 << 20
	MaxDirectoryEntries  = 5000
	FileOperationTimeout = 2 * time.Minute
)

var (
	ErrFileTooLarge     = errors.New("file exceeds the size limit")
	ErrFileNotText      = errors.New("only UTF-8 text files can be edited")
	ErrFileNotRegular   = errors.New("not a regular file or refusing to replace a symbolic link")
	ErrFileExists       = errors.New("file already exists")
	ErrFileChanged      = errors.New("file changed since it was opened")
	ErrFilePath         = errors.New("invalid remote path")
	ErrFilesUnsupported = errors.New("container files require a running Linux container with sh and core file utilities on a Linux host")
)

type RemoteFileEntry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	Mode       string    `json:"mode"`
	IsDir      bool      `json:"is_dir"`
	IsFile     bool      `json:"is_file"`
	IsSymlink  bool      `json:"is_symlink"`
}

type RemoteFileList struct {
	Path       string            `json:"path"`
	Parent     string            `json:"parent"`
	Entries    []RemoteFileEntry `json:"entries"`
	Truncated  bool              `json:"truncated"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

type RemoteFiles interface {
	List(string) (RemoteFileList, error)
	Read(string, io.Writer, int64) error
	Write(string, io.Reader, int64, bool) error
	Close() error
}

func CleanRemotePath(value string, allowHome bool) (string, error) {
	if value == "" && allowHome {
		return ".", nil
	}
	if len(value) > 4096 || strings.ContainsRune(value, 0) || !utf8.ValidString(value) {
		return "", ErrFilePath
	}
	if len(value) >= 3 && value[1] == ':' && value[2] == '\\' {
		value = strings.ReplaceAll(value, "\\", "/")
	}
	drivePath := len(value) >= 3 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1:3] == ":/"
	if !path.IsAbs(value) && !drivePath {
		return "", ErrFilePath
	}
	cleaned := path.Clean(value)
	if drivePath && len(cleaned) == 2 {
		cleaned += "/"
	}
	if len(cleaned) == 3 && cleaned[0] == '/' && cleaned[2] == ':' {
		cleaned += "/"
	}
	return cleaned, nil
}

func remoteFileParent(directory string) string {
	if len(directory) == 3 && directory[1:] == ":/" {
		return directory
	}
	if len(directory) == 4 && directory[0] == '/' && directory[2:] == ":/" {
		return directory
	}
	parent := path.Dir(directory)
	if len(parent) == 2 && parent[1] == ':' {
		parent += "/"
	}
	if len(parent) == 3 && parent[0] == '/' && parent[2] == ':' {
		parent += "/"
	}
	return parent
}

func ValidUploadName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && utf8.ValidString(name) && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func SortRemoteFiles(list *RemoteFileList) {
	sort.Slice(list.Entries, func(first, second int) bool {
		if list.Entries[first].IsDir != list.Entries[second].IsDir {
			return list.Entries[first].IsDir
		}
		return strings.ToLower(list.Entries[first].Name) < strings.ToLower(list.Entries[second].Name)
	})
}

func FileRevision(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func ReadRemoteText(store RemoteFiles, filename string) ([]byte, error) {
	var content bytes.Buffer
	if err := store.Read(filename, &content, MaxTextFileSize); err != nil {
		return nil, err
	}
	if !utf8.Valid(content.Bytes()) || bytes.IndexByte(content.Bytes(), 0) >= 0 {
		return nil, ErrFileNotText
	}
	return content.Bytes(), nil
}

func SaveRemoteText(store RemoteFiles, filename, content, revision string) error {
	if len(content) > MaxTextFileSize {
		return ErrFileTooLarge
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return ErrFileNotText
	}
	current, err := ReadRemoteText(store, filename)
	if err != nil {
		return err
	}
	if revision == "" || FileRevision(current) != revision {
		return ErrFileChanged
	}
	return store.Write(filename, strings.NewReader(content), int64(len(content)), true)
}

type sftpFiles struct {
	transport *ssh.Client
	client    *sftp.Client
	session   *ssh.Session
	context   context.Context
	stop      func() bool
	windows   bool
}

func NewSFTPFiles(ctx context.Context, client *ssh.Client, serverType string) (RemoteFiles, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	failed := true
	defer func() {
		if failed {
			stop()
			_ = session.Close()
		}
	}()
	input, err := session.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := session.StdoutPipe()
	if err != nil {
		return nil, err
	}
	session.Stderr = io.Discard
	if err := session.RequestSubsystem("sftp"); err != nil {
		return nil, err
	}
	remote, err := sftp.NewClientPipe(output, input)
	if err != nil {
		return nil, err
	}
	failed = false
	return &sftpFiles{transport: client, client: remote, session: session, context: ctx, stop: stop, windows: serverType == "windows"}, nil
}

func (store *sftpFiles) Close() error {
	if store.stop != nil {
		store.stop()
	}
	err := store.client.Close()
	if store.session != nil {
		_ = store.session.Close()
	}
	return err
}

func (store *sftpFiles) List(directory string) (RemoteFileList, error) {
	canonical, err := store.client.RealPath(directory)
	if err != nil {
		return RemoteFileList{}, err
	}
	entries, err := store.client.ReadDirContext(store.context, canonical)
	if err != nil {
		return RemoteFileList{}, err
	}
	result := RemoteFileList{Path: canonical, Parent: remoteFileParent(canonical), Entries: []RemoteFileEntry{}, Truncated: len(entries) > MaxDirectoryEntries}
	if result.Truncated {
		entries = entries[:MaxDirectoryEntries]
	}
	for _, entry := range entries {
		filename := path.Join(canonical, entry.Name())
		symlink := entry.Mode()&os.ModeSymlink != 0
		details := entry
		if symlink {
			if target, err := store.client.Stat(filename); err == nil {
				details = target
			}
		}
		result.Entries = append(result.Entries, RemoteFileEntry{Name: entry.Name(), Path: filename, Size: details.Size(), ModifiedAt: details.ModTime().UTC(), Mode: details.Mode().String(), IsDir: details.IsDir(), IsFile: details.Mode().IsRegular(), IsSymlink: symlink})
	}
	SortRemoteFiles(&result)
	return result, nil
}

func (store *sftpFiles) Read(filename string, output io.Writer, limit int64) error {
	info, err := store.client.Stat(filename)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrFileNotRegular
	}
	if info.Size() > limit {
		return ErrFileTooLarge
	}
	file, err := store.client.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrFileNotRegular
	}
	copied, err := io.Copy(output, io.LimitReader(file, limit+1))
	if copied > limit {
		return ErrFileTooLarge
	}
	return err
}

func (store *sftpFiles) Write(filename string, input io.Reader, size int64, overwrite bool) error {
	info, err := store.client.Lstat(filename)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if exists && !overwrite {
		return ErrFileExists
	}
	if exists && !info.Mode().IsRegular() {
		return ErrFileNotRegular
	}
	temporary := path.Join(path.Dir(filename), ".server-monitor-"+uuid.NewString())
	file, err := store.client.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer store.client.Remove(temporary)
	defer file.Close()
	mode := os.FileMode(0644)
	if exists {
		mode = info.Mode().Perm()
		if metadata, ok := info.Sys().(*sftp.FileStat); ok && !store.windows {
			if err := file.Chown(int(metadata.UID), int(metadata.GID)); err != nil {
				var status *sftp.StatusError
				if !errors.As(err, &status) || status.Code != 8 {
					return err
				}
			}
		}
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	copied, err := io.Copy(file, io.LimitReader(input, size+1))
	if err != nil {
		return err
	}
	if copied != size {
		return io.ErrUnexpectedEOF
	}
	if err := file.Close(); err != nil {
		return err
	}
	if overwrite {
		err = store.client.PosixRename(temporary, filename)
		var status *sftp.StatusError
		if !errors.As(err, &status) || status.Code != 8 {
			return err
		}
	}
	err = store.client.Rename(temporary, filename)
	if err != nil && !overwrite {
		if _, existsErr := store.client.Lstat(filename); existsErr == nil {
			return ErrFileExists
		}
	}
	return err
}
