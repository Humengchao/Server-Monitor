package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func TestRemoteFilePaths(t *testing.T) {
	for _, value := range []string{"/", "/etc/a b'$.txt", "/tmp/../etc", "C:/Users/example", "/C:/Users/example"} {
		if _, err := CleanRemotePath(value, false); err != nil {
			t.Errorf("valid path %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "relative", "../etc", "~/.ssh", "/bad\x00name", strings.Repeat("x", 4097)} {
		if _, err := CleanRemotePath(value, false); !errors.Is(err, ErrFilePath) {
			t.Errorf("accepted path %q", value)
		}
	}
	if actual, err := CleanRemotePath("", true); err != nil || actual != "." {
		t.Fatalf("home = %q, %v", actual, err)
	}
	for _, name := range []string{".env", "a b'$.txt", "配置.json"} {
		if !ValidUploadName(name) {
			t.Errorf("valid name rejected: %q", name)
		}
	}
	for _, name := range []string{"", ".", "..", "../secret", "a/b", "a\\b", "bad\x00", "bad\nname"} {
		if ValidUploadName(name) {
			t.Errorf("accepted upload name: %q", name)
		}
	}
	for _, id := range []string{"abc", "--help", "deadbeef1234;id", strings.Repeat("a", 65)} {
		if ValidFileContainerID(id) {
			t.Errorf("accepted container id: %s", id)
		}
	}
}

func TestWindowsFileRoots(t *testing.T) {
	for _, value := range []string{"C:/", "C:\\", "/C:/"} {
		actual, err := CleanRemotePath(value, false)
		if err != nil || !strings.HasSuffix(actual, ":/") {
			t.Fatalf("drive root %q became %q, %v", value, actual, err)
		}
		if parent := remoteFileParent(actual); parent != actual {
			t.Fatalf("drive root parent = %q, want %q", parent, actual)
		}
	}
	if parent := remoteFileParent("C:/Users"); parent != "C:/" {
		t.Fatalf("parent = %q", parent)
	}
}

func sftpTestStore(t *testing.T) *sftpFiles {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	server, err := sftp.NewServer(serverConn)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(); _ = server.Close() }()
	client, err := sftp.NewClientPipe(clientConn, clientConn)
	if err != nil {
		t.Fatal(err)
	}
	store := &sftpFiles{client: client, context: context.Background(), windows: runtime.GOOS == "windows"}
	t.Cleanup(func() { _ = store.Close(); _ = serverConn.Close() })
	return store
}

type interruptedFileReader struct{}

func (interruptedFileReader) Read([]byte) (int, error) {
	return 0, errors.New("interrupted test transfer")
}

func TestSFTPFilesRoundTripAndAtomicOverwrite(t *testing.T) {
	directory := filepath.ToSlash(t.TempDir())
	filename := directory + "/config 'quoted'.txt"
	store := sftpTestStore(t)
	original := "hello\r\n配置=true\r\n"
	if err := store.Write(filename, strings.NewReader(original), int64(len(original)), false); err != nil {
		t.Fatal(err)
	}
	content, err := ReadRemoteText(store, filename)
	if err != nil || string(content) != original {
		t.Fatalf("read = %q, %v", content, err)
	}
	listing, err := store.List(directory)
	if err != nil || len(listing.Entries) != 1 || !listing.Entries[0].IsFile {
		t.Fatalf("listing = %+v, %v", listing, err)
	}
	if err := store.Write(filename, strings.NewReader("bad"), 3, false); !errors.Is(err, ErrFileExists) {
		t.Fatalf("overwrite without consent = %v", err)
	}
	if err := SaveRemoteText(store, filename, "bad", strings.Repeat("0", 64)); !errors.Is(err, ErrFileChanged) {
		t.Fatalf("stale revision = %v", err)
	}
	interrupted := io.MultiReader(strings.NewReader("partial"), interruptedFileReader{})
	if err := store.Write(filename, interrupted, 100, true); err == nil {
		t.Fatal("interrupted upload succeeded")
	}
	content, err = ReadRemoteText(store, filename)
	if err != nil || string(content) != original {
		t.Fatalf("failed write changed original: %q, %v", content, err)
	}
	if err := SaveRemoteText(store, filename, "", FileRevision(content)); err != nil {
		t.Fatal(err)
	}
	content, err = ReadRemoteText(store, filename)
	if err != nil || len(content) != 0 {
		t.Fatalf("empty text = %q, %v", content, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary file was not removed: %v, %v", entries, err)
	}
}

func TestSFTPFileReadLimitsAndTypes(t *testing.T) {
	directory := filepath.ToSlash(t.TempDir())
	store := sftpTestStore(t)
	binary := directory + "/binary"
	if err := os.WriteFile(binary, []byte{0, 1, 2, 255}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRemoteText(store, binary); !errors.Is(err, ErrFileNotText) {
		t.Fatalf("binary edit = %v", err)
	}
	var downloaded bytes.Buffer
	if err := store.Read(binary, &downloaded, 4); err != nil || !bytes.Equal(downloaded.Bytes(), []byte{0, 1, 2, 255}) {
		t.Fatalf("binary download = %v", err)
	}
	if err := store.Read(binary, io.Discard, 3); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("size limit = %v", err)
	}
	if err := store.Read(directory, io.Discard, 1024); !errors.Is(err, ErrFileNotRegular) {
		t.Fatalf("directory read = %v", err)
	}
	if err := store.Write(directory, strings.NewReader("bad"), 3, true); !errors.Is(err, ErrFileNotRegular) {
		t.Fatalf("directory overwrite = %v", err)
	}
	large := directory + "/large.txt"
	if err := os.WriteFile(large, bytes.Repeat([]byte("a"), MaxTextFileSize+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRemoteText(store, large); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("large edit = %v", err)
	}
}

func TestParseContainerFileNamesAndTruncation(t *testing.T) {
	output := "/etc\x0081a4 7 1700000000 644 0\x00a\n'b.txt\x0041ed 0 1700000000 755 0\x00directory\x00TRUNCATED\x00"
	listing, err := parseContainerFileList([]byte(output))
	if err != nil || !listing.Truncated || len(listing.Entries) != 2 {
		t.Fatalf("listing = %+v, %v", listing, err)
	}
	if !listing.Entries[0].IsDir || listing.Entries[1].Name != "a\n'b.txt" || !listing.Entries[1].IsFile {
		t.Fatalf("entries = %+v", listing.Entries)
	}
	for _, invalid := range []string{"bad", "/tmp\x00bad metadata\x00name\x00", "/tmp\x0081a4 1 2 644 0\x00../escape\x00"} {
		if _, err := parseContainerFileList([]byte(invalid)); err == nil {
			t.Errorf("accepted malformed listing %q", invalid)
		}
	}
}

func fileTestShell(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		shell := filepath.Join(os.Getenv("ProgramFiles"), "Git", "bin", "bash.exe")
		if _, err := os.Stat(shell); err == nil {
			return shell
		}
		t.Skip("Git Bash unavailable")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	return shell
}

func TestFileArgumentQuoting(t *testing.T) {
	shell := fileTestShell(t)
	for _, value := range []string{"a'b c", "$(echo injected); $HOME", "a\nb\t\"c"} {
		output, err := exec.Command(shell, "-c", "printf '%s' "+QuoteFileArgument(value)).Output()
		if err != nil || string(output) != value {
			t.Fatalf("argument %q became %q, %v", value, output, err)
		}
	}
}

func TestContainerFileScripts(t *testing.T) {
	shell := fileTestShell(t)
	directory := filepath.ToSlash(t.TempDir())
	filename := directory + "/a' $HOME;.txt"
	temporary := directory + "/.server-monitor-test"
	run := func(script, input string, arguments ...string) ([]byte, error) {
		command := exec.Command(shell, append([]string{"-c", script, "sh"}, arguments...)...)
		command.Stdin = strings.NewReader(input)
		return command.Output()
	}
	if _, err := run(writeContainerFileScript, "hello", filename, temporary, "0", "5"); err != nil {
		t.Fatal(err)
	}
	output, err := run(listContainerFilesScript, "", directory)
	if err != nil {
		t.Fatal(err)
	}
	listing, err := parseContainerFileList(output)
	if err != nil || len(listing.Entries) != 1 || listing.Entries[0].Name != "a' $HOME;.txt" {
		t.Fatalf("listing = %+v, %v", listing, err)
	}
	if _, err := run(writeContainerFileScript, "clobber", filename, temporary, "0", "7"); err == nil {
		t.Fatal("overwrite was not blocked")
	}
	if _, err := run(writeContainerFileScript, "partial", filename, temporary, "1", "999"); err == nil {
		t.Fatal("incomplete transfer succeeded")
	}
	content, err := os.ReadFile(filename)
	if err != nil || string(content) != "hello" {
		t.Fatalf("original was damaged: %q, %v", content, err)
	}
	if _, err := run(writeContainerFileScript, "updated", filename, temporary, "1", "7"); err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(filename)
	if err != nil || string(content) != "updated" {
		t.Fatalf("replacement = %q, %v", content, err)
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatalf("temporary file remains: %v", err)
	}
}
