package services

import (
	"bytes"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"
	"time"
)

type containerNames struct {
	pending   []byte
	directory string
	names     []string
	offset    int
	seen      int
	limit     int
	done      bool
}

func (collector *containerNames) Write(data []byte) (int, error) {
	size := len(data)
	collector.pending = append(collector.pending, data...)
	for {
		index := bytes.IndexByte(collector.pending, 0)
		if index < 0 {
			break
		}
		value := string(collector.pending[:index])
		collector.pending = collector.pending[index+1:]
		if collector.directory == "" {
			collector.directory = value
			continue
		}
		if collector.seen >= collector.offset {
			collector.names = append(collector.names, strings.TrimPrefix(value, "./"))
		}
		collector.seen++
		if len(collector.names) > collector.limit {
			collector.done = true
			return size, io.EOF
		}
	}
	if len(collector.pending) > 8192 {
		return size, ErrFilePath
	}
	return size, nil
}
func containerEntry(filename, line string) (RemoteFileEntry, error) {
	fields := strings.Fields(line)
	if len(fields) != 4 {
		return RemoteFileEntry{}, errors.New("invalid file metadata")
	}
	mode, err := strconv.ParseUint(fields[0], 16, 32)
	if err != nil {
		return RemoteFileEntry{}, err
	}
	size, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return RemoteFileEntry{}, err
	}
	modified, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return RemoteFileEntry{}, err
	}
	return RemoteFileEntry{Name: path.Base(filename), Path: filename, Size: size, ModifiedAt: time.Unix(modified, 0).UTC(), Mode: fields[3], IsDir: mode&0170000 == 0040000, IsFile: mode&0170000 == 0100000, IsSymlink: mode&0170000 == 0120000}, nil
}
func (store *containerFiles) Page(directory string, offset, limit int) (RemoteFileList, error) {
	if directory == "." {
		directory = "/"
	}
	collector := &containerNames{offset: offset, limit: limit}
	err := store.run(`cd -P "$1" || exit 46
printf '%s\000' "$PWD"
find . -mindepth 1 -maxdepth 1 -print0`, []string{directory}, nil, collector, int64(offset+limit+2)*8192)
	if err != nil && !collector.done {
		return RemoteFileList{}, err
	}
	if _, err = CleanRemotePath(collector.directory, false); err != nil {
		return RemoteFileList{}, err
	}
	names := collector.names
	if len(names) > limit {
		names = names[:limit]
	}
	entries := []RemoteFileEntry{}
	if len(names) > 0 {
		arguments := []string{collector.directory}
		arguments = append(arguments, names...)
		var output bytes.Buffer
		err = store.run(`cd -P "$1" || exit 46
shift
stat -c '%f %s %Y %a' -- "$@"`, arguments, nil, &output, int64(limit)*256)
		if err != nil {
			return RemoteFileList{}, err
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if len(lines) != len(names) {
			return RemoteFileList{}, errors.New("directory changed during listing; refresh")
		}
		for index, name := range names {
			entry, parseErr := containerEntry(path.Join(collector.directory, name), lines[index])
			if parseErr != nil {
				return RemoteFileList{}, parseErr
			}
			if entry.IsSymlink {
				var target bytes.Buffer
				if statErr := store.run(`stat -L -c '%f %s %Y %a' -- "$1"`, []string{entry.Path}, nil, &target, 256); statErr == nil {
					if resolved, parseErr := containerEntry(entry.Path, target.String()); parseErr == nil {
						entry = resolved
						entry.IsSymlink = true
					}
				}
			}
			entries = append(entries, entry)
		}
	}
	result := pageResult(collector.directory, entries, offset, limit)
	if collector.done {
		result.NextCursor = strconv.Itoa(offset + limit)
	}
	return result, nil
}
func (store *containerFiles) Metadata(filename string) (RemoteFileEntry, error) {
	var output bytes.Buffer
	err := store.run(`[ -e "$1" ] || [ -L "$1" ] || exit 46
stat -c '%f %s %Y %a' -- "$1"`, []string{filename}, nil, &output, 1024)
	if err != nil {
		return RemoteFileEntry{}, err
	}
	return containerEntry(filename, output.String())
}
func (store *containerFiles) Mkdir(directory string, private bool) error {
	script := `[ ! -e "$1" ] && [ ! -L "$1" ] || exit 44
mkdir -- "$1"`
	if private {
		script = `if [ -e "$1" ] || [ -L "$1" ]; then
 [ -d "$1" ] && [ ! -L "$1" ] || exit 45
else
 (umask 077; mkdir -- "$1") || exit 47
fi
chmod 700 -- "$1"`
	}
	return store.run(script, []string{directory}, nil, io.Discard, 1024)
}
func (store *containerFiles) Rename(source, destination string) error {
	return store.run(`[ -e "$1" ] || [ -L "$1" ] || exit 46
[ ! -e "$2" ] && [ ! -L "$2" ] || exit 44
mv -nT -- "$1" "$2" || exit 1
[ ! -e "$1" ] && [ ! -L "$1" ] || exit 44`, []string{source, destination}, nil, io.Discard, 1024)
}
func (store *containerFiles) Remove(filename string) error {
	return store.run(`[ -e "$1" ] || [ -L "$1" ] || exit 46
if [ -d "$1" ] && [ ! -L "$1" ]; then rmdir -- "$1"; else rm -- "$1"; fi`, []string{filename}, nil, io.Discard, 1024)
}

var _ ManagedFiles = (*containerFiles)(nil)
var _ ManagedFiles = (*sftpFiles)(nil)
