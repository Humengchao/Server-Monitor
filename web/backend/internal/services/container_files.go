package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

var fileContainerID = regexp.MustCompile(`^[a-fA-F0-9]{12,64}$`)

func ValidFileContainerID(value string) bool { return fileContainerID.MatchString(value) }

func QuoteFileArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

type containerFiles struct {
	context     context.Context
	client      *ssh.Client
	command     string
	containerID string
}

func NewContainerFiles(ctx context.Context, client *ssh.Client, containerID, serverType string) (RemoteFiles, error) {
	if !ValidFileContainerID(containerID) {
		return nil, ErrFilePath
	}
	if serverType == "windows" {
		return nil, ErrFilesUnsupported
	}
	command := "docker"
	probe := " inspect --format '{{.State.Status}}' " + containerID
	output, _, err := RunCmdBounded(client, command+probe, 10*time.Second, 1024)
	if err != nil {
		command = "sudo -n docker"
		output, _, err = RunCmdBounded(client, command+probe, 10*time.Second, 1024)
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(output) != "running" {
		return nil, ErrFilesUnsupported
	}
	return &containerFiles{context: ctx, client: client, command: command, containerID: containerID}, nil
}

func (store *containerFiles) Close() error { return nil }

type fileOutputWriter struct {
	output    io.Writer
	remaining int64
	exceeded  bool
	close     func() error
}

func (writer *fileOutputWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > writer.remaining {
		writer.exceeded = true
		_ = writer.close()
		return 0, ErrFileTooLarge
	}
	written, err := writer.output.Write(data)
	if err != nil {
		_ = writer.close()
	}
	writer.remaining -= int64(written)
	return written, err
}

func (store *containerFiles) run(script string, arguments []string, input io.Reader, output io.Writer, limit int64) error {
	if err := store.context.Err(); err != nil {
		return err
	}
	session, err := store.client.NewSession()
	if err != nil {
		return err
	}
	defer session.Close()
	stop := context.AfterFunc(store.context, func() { _ = session.Close() })
	defer stop()
	command := store.command + " exec -i " + store.containerID + " sh -c " + QuoteFileArgument(script) + " sh"
	for _, argument := range arguments {
		command += " " + QuoteFileArgument(argument)
	}
	writer := &fileOutputWriter{output: output, remaining: limit, close: session.Close}
	session.Stdin, session.Stdout = input, writer
	session.Stderr = &syncLimitedWriter{limit: 4096}
	err = session.Run(command)
	if store.context.Err() != nil {
		return store.context.Err()
	}
	if writer.exceeded {
		return ErrFileTooLarge
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitStatus() {
		case 44:
			return ErrFileExists
		case 45:
			return ErrFileNotRegular
		case 46:
			return os.ErrNotExist
		case 47:
			return os.ErrPermission
		}
	}
	return err
}

const listContainerFilesScript = `command -v stat >/dev/null || exit 127
cd -P "$1" || exit 1
printf '%s\000' "$PWD"
count=0
for entry in .[!.]* ..?* *; do
    [ -e "$entry" ] || [ -L "$entry" ] || continue
    count=$((count + 1))
    [ "$count" -le 5000 ] || { printf 'TRUNCATED\000'; break; }
    details=$(stat -L -c '%f %s %Y %a' -- "$entry") || details='0 0 0 0'
    link=0
    [ ! -L "$entry" ] || link=1
    printf '%s %s\000%s\000' "$details" "$link" "$entry"
done`

func parseContainerFileList(output []byte) (RemoteFileList, error) {
	fields := bytes.Split(output, []byte{0})
	if len(fields) < 2 || len(fields[len(fields)-1]) != 0 {
		return RemoteFileList{}, fmt.Errorf("invalid directory response")
	}
	canonical, err := CleanRemotePath(string(fields[0]), false)
	if err != nil {
		return RemoteFileList{}, err
	}
	result := RemoteFileList{Path: canonical, Parent: remoteFileParent(canonical), Entries: []RemoteFileEntry{}}
	for index := 1; index < len(fields)-1; index += 2 {
		if string(fields[index]) == "TRUNCATED" {
			result.Truncated = true
			break
		}
		if index+1 >= len(fields)-1 {
			return RemoteFileList{}, fmt.Errorf("incomplete directory entry")
		}
		metadata := strings.Fields(string(fields[index]))
		if len(metadata) != 5 {
			return RemoteFileList{}, fmt.Errorf("invalid file metadata")
		}
		mode, modeErr := strconv.ParseUint(metadata[0], 16, 32)
		size, sizeErr := strconv.ParseInt(metadata[1], 10, 64)
		modified, modifiedErr := strconv.ParseInt(metadata[2], 10, 64)
		name := string(fields[index+1])
		if modeErr != nil || sizeErr != nil || modifiedErr != nil || name == "" || strings.Contains(name, "/") || name == "." || name == ".." {
			return RemoteFileList{}, fmt.Errorf("invalid file entry")
		}
		result.Entries = append(result.Entries, RemoteFileEntry{Name: name, Path: path.Join(canonical, name), Size: size, ModifiedAt: time.Unix(modified, 0).UTC(), Mode: metadata[3], IsDir: mode&0170000 == 0040000, IsFile: mode&0170000 == 0100000, IsSymlink: metadata[4] == "1"})
	}
	SortRemoteFiles(&result)
	return result, nil
}

func (store *containerFiles) List(directory string) (RemoteFileList, error) {
	if directory == "." {
		directory = "/"
	}
	var output bytes.Buffer
	if err := store.run(listContainerFilesScript, []string{directory}, nil, &output, 4<<20); err != nil {
		return RemoteFileList{}, err
	}
	return parseContainerFileList(output.Bytes())
}

func (store *containerFiles) Read(filename string, output io.Writer, limit int64) error {
	return store.run(`[ -f "$1" ] || exit 45
cat -- "$1"`, []string{filename}, nil, output, limit)
}

const writeContainerFileScript = `if [ -e "$1" ] || [ -L "$1" ]; then
    [ "$3" = 1 ] || exit 44
    [ ! -L "$1" ] && [ -f "$1" ] || exit 45
fi
umask 077
(set -C; : > "$2") || exit 1
trap 'rm -f -- "$2"' EXIT HUP INT TERM
if [ -f "$1" ]; then
    ownership=$(stat -c '%u:%g' -- "$1") || exit 1
    permissions=$(stat -c '%a' -- "$1") || exit 1
    chown "$ownership" "$2" || exit 1
    chmod "$permissions" "$2" || exit 1
else
    chmod 644 "$2" || exit 1
fi
cat > "$2" || exit 1
[ "$(wc -c < "$2")" -eq "$4" ] || exit 1
if [ "$3" = 1 ]; then
    [ ! -L "$1" ] || exit 45
    [ ! -e "$1" ] || [ -f "$1" ] || exit 45
    mv -f -- "$2" "$1" || exit 1
else
    ln -- "$2" "$1" || exit 44
fi`

func (store *containerFiles) Write(filename string, input io.Reader, size int64, overwrite bool) error {
	replace := "0"
	if overwrite {
		replace = "1"
	}
	temporary := path.Join(path.Dir(filename), ".server-monitor-"+uuid.NewString())
	return store.run(writeContainerFileScript, []string{filename, temporary, replace, strconv.FormatInt(size, 10)}, input, io.Discard, 4096)
}
