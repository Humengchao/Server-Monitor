package services

import (
	"errors"
	"github.com/google/uuid"
	"os"
	"path"
	"strings"
	"time"
)

func (store *sftpFiles) Metadata(filename string) (RemoteFileEntry, error) {
	info, err := store.client.Lstat(filename)
	if err != nil {
		return RemoteFileEntry{}, err
	}
	return fileEntry(filename, info), nil
}
func (store *sftpFiles) Mkdir(directory string, private bool) error {
	err := store.client.Mkdir(directory)
	if err != nil {
		info, statErr := store.client.Lstat(directory)
		if !private && statErr == nil {
			return ErrFileExists
		}
		if !private || statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return err
		}
	}
	if private {
		return store.client.Chmod(directory, 0700)
	}
	return nil
}
func (store *sftpFiles) Rename(source, destination string) error {
	if _, err := store.client.Lstat(destination); err == nil {
		return ErrFileExists
	} else if !os.IsNotExist(err) {
		return err
	}
	return store.client.Rename(source, destination)
}
func (store *sftpFiles) Remove(filename string) error {
	info, err := store.client.Lstat(filename)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return store.client.RemoveDirectory(filename)
	}
	return store.client.Remove(filename)
}
func BackupRemoteText(store RemoteFiles, filename string, content []byte) (string, error) {
	managed, ok := store.(ManagedFiles)
	if !ok {
		return "", errors.New("file backups unsupported")
	}
	directory := path.Join(path.Dir(filename), ".server-monitor-backups")
	if err := managed.Mkdir(directory, true); err != nil {
		return "", err
	}
	name := path.Base(filename)
	if len(name) > 80 {
		name = "file"
	}
	backup := path.Join(directory, name+"."+time.Now().UTC().Format("20060102T150405")+"."+uuid.NewString())
	if err := store.Write(backup, strings.NewReader(string(content)), int64(len(content)), false); err != nil {
		return "", err
	}
	return backup, nil
}
