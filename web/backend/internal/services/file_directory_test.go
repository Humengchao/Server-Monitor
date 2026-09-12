package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type countingDirectoryReader struct {
	io.Reader
	size int
}

func (reader *countingDirectoryReader) Read(value []byte) (int, error) {
	count, err := reader.Reader.Read(value)
	reader.size += count
	return count, err
}
func testDirectoryPage(testContext *testing.T, directory string, offset, limit int) (RemoteFileList, int) {
	testContext.Helper()
	client, remote := net.Pipe()
	defer client.Close()
	server, err := sftp.NewServer(remote)
	if err != nil {
		testContext.Fatal(err)
	}
	go func() { _ = server.Serve(); _ = server.Close() }()
	reader := &countingDirectoryReader{Reader: client}
	listing, err := readDirectoryPage(reader, client, directory, offset, limit)
	if err != nil {
		testContext.Fatal(err)
	}
	return listing, reader.size
}
func TestSFTPDirectoryPagesReadOnlyRequiredBatches(testContext *testing.T) {
	directory := filepath.ToSlash(testContext.TempDir())
	for index := 0; index < 350; index++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("file-%03d.txt", index)), []byte("text"), 0600); err != nil {
			testContext.Fatal(err)
		}
	}
	first, firstBytes := testDirectoryPage(testContext, directory, 0, 30)
	full, fullBytes := testDirectoryPage(testContext, directory, 0, 500)
	if len(first.Entries) != 30 || first.NextCursor != "30" || len(full.Entries) != 350 || firstBytes*2 >= fullBytes {
		testContext.Fatalf("not bounded: first=%d bytes=%d full=%d bytes=%d", len(first.Entries), firstBytes, len(full.Entries), fullBytes)
	}
	seen := map[string]bool{}
	offset := 0
	for {
		listing, _ := testDirectoryPage(testContext, directory, offset, 30)
		for _, entry := range listing.Entries {
			if seen[entry.Name] {
				testContext.Fatal("duplicate", entry.Name)
			}
			seen[entry.Name] = true
		}
		if listing.NextCursor == "" {
			break
		}
		offset, _ = strconv.Atoi(listing.NextCursor)
	}
	if len(seen) != 350 {
		testContext.Fatal("missing entries", len(seen))
	}
}
func TestSafeFileChangesAndBackups(testContext *testing.T) {
	store := sftpTestStore(testContext)
	directory := filepath.ToSlash(testContext.TempDir())
	filename := directory + "/config.yaml"
	if err := store.Write(filename, strings.NewReader("original"), 8, false); err != nil {
		testContext.Fatal(err)
	}
	backup, err := BackupRemoteText(store, filename, []byte("original"))
	if err != nil {
		testContext.Fatal(err)
	}
	content, err := os.ReadFile(backup)
	if err != nil || string(content) != "original" {
		testContext.Fatal(string(content), err)
	}
	if err = store.Write(directory+"/exists", strings.NewReader("keep"), 4, false); err != nil {
		testContext.Fatal(err)
	}
	if err = store.Rename(filename, directory+"/exists"); !errors.Is(err, ErrFileExists) {
		testContext.Fatal("clobber", err)
	}
	if err = store.Rename(filename, directory+"/renamed"); err != nil {
		testContext.Fatal(err)
	}
	if err = store.Mkdir(directory+"/empty", false); err != nil {
		testContext.Fatal(err)
	}
	if err = store.Remove(directory + "/empty"); err != nil {
		testContext.Fatal(err)
	}
	if err = store.Remove(directory + "/.server-monitor-backups"); err == nil {
		testContext.Fatal("nonempty directory deleted")
	}
	if err = store.Remove(directory + "/renamed"); err != nil {
		testContext.Fatal(err)
	}
	if content, err = os.ReadFile(backup); err != nil || string(content) != "original" {
		testContext.Fatal("backup damaged", err)
	}
}
func testCommandSSH(testContext *testing.T) (*ssh.Client, *atomic.Int32) {
	testContext.Helper()
	shell := fileTestShell(testContext)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		testContext.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		testContext.Fatal(err)
	}
	config := &ssh.ServerConfig{NoClientAuth: true}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		testContext.Fatal(err)
	}
	testContext.Cleanup(func() { listener.Close() })
	calls := &atomic.Int32{}
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		_, channels, requests, handshakeErr := ssh.NewServerConn(connection, config)
		if handshakeErr != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for incoming := range channels {
			channel, requests, acceptErr := incoming.Accept()
			if acceptErr != nil {
				continue
			}
			go func() {
				defer channel.Close()
				for request := range requests {
					if request.Type != "exec" {
						request.Reply(false, nil)
						continue
					}
					var payload struct{ Command string }
					if ssh.Unmarshal(request.Payload, &payload) != nil {
						request.Reply(false, nil)
						return
					}
					parts := strings.SplitN(payload.Command, " ", 5)
					if len(parts) != 5 || parts[0] != "docker" {
						request.Reply(false, nil)
						return
					}
					request.Reply(true, nil)
					calls.Add(1)
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					command := exec.CommandContext(ctx, shell, "-c", parts[4])
					command.Stdin = channel
					command.Stdout = channel
					command.Stderr = channel.Stderr()
					runErr := command.Run()
					cancel()
					status := uint32(0)
					if runErr != nil {
						status = 1
						var exit *exec.ExitError
						if errors.As(runErr, &exit) && exit.ExitCode() >= 0 {
							status = uint32(exit.ExitCode())
						}
					}
					channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
					return
				}
			}()
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{User: "test", HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
	if err != nil {
		testContext.Fatal(err)
	}
	testContext.Cleanup(func() { client.Close() })
	return client, calls
}
func TestContainerPagesBatchStatAndSafeOperations(testContext *testing.T) {
	client, calls := testCommandSSH(testContext)
	store := &containerFiles{client: client, context: context.Background(), command: "docker", containerID: strings.Repeat("a", 64)}
	directory := filepath.ToSlash(testContext.TempDir())
	for index := 0; index < 130; index++ {
		if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("file-%03d.txt", index)), []byte("hello"), 0600); err != nil {
			testContext.Fatal(err)
		}
	}
	listing, err := store.Page(directory, 0, 50)
	if err != nil {
		testContext.Fatal(err)
	}
	if len(listing.Entries) != 50 || listing.NextCursor != "50" || calls.Load() != 2 {
		testContext.Fatalf("page=%+v calls=%d", listing, calls.Load())
	}
	next, err := store.Page(directory, 50, 50)
	if err != nil || len(next.Entries) != 50 {
		testContext.Fatal(err, len(next.Entries))
	}
	filename := directory + "/file-000.txt"
	entry, err := store.Metadata(filename)
	if err != nil || !entry.IsFile || entry.Size != 5 {
		testContext.Fatal(entry, err)
	}
	if err = store.Rename(filename, directory+"/file-001.txt"); !errors.Is(err, ErrFileExists) {
		testContext.Fatal("overwrite allowed", err)
	}
	if err = store.Rename(filename, directory+"/quoted ' $name.txt"); err != nil {
		testContext.Fatal(err)
	}
	backup, err := BackupRemoteText(store, directory+"/quoted ' $name.txt", []byte("hello"))
	if err != nil {
		testContext.Fatal(err)
	}
	if _, err = store.Metadata(backup); err != nil {
		testContext.Fatal(err)
	}
	if err = store.Remove(directory); err == nil {
		testContext.Fatal("nonempty directory removed")
	}
	if err = store.Mkdir(directory+"/empty", false); err != nil {
		testContext.Fatal(err)
	}
	if err = store.Remove(directory + "/empty"); err != nil {
		testContext.Fatal(err)
	}
}
