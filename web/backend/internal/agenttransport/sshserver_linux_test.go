//go:build linux

package agenttransport

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestLinuxExecExitStatusAndSeparateStderr(t *testing.T) {
	f := newTunnel(t)
	client := f.dial(t)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	err = session.Run("printf 'hello 中文'; printf 'failure' >&2; exit 7")
	exit, ok := err.(*ssh.ExitError)
	if !ok || exit.ExitStatus() != 7 || stdout.String() != "hello 中文" || stderr.String() != "failure" {
		t.Fatalf("%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

func TestLinuxPTYResizeAndControlC(t *testing.T) {
	f := newTunnel(t)
	client := f.dial(t)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{ssh.ECHO: 0}); err != nil {
		t.Fatal(err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Start("stty size; read x; stty size; trap 'echo INTERRUPTED; exit 0' INT; echo WAITING; sleep 60"); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 20)
	go func() {
		scan := bufio.NewScanner(stdout)
		for scan.Scan() {
			lines <- strings.TrimSpace(scan.Text())
		}
		close(lines)
	}()
	read := func(want string) {
		t.Helper()
		select {
		case line := <-lines:
			if line != want {
				t.Fatalf("got %q want %q", line, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("PTY output timeout", want)
		}
	}
	read("24 80")
	if err := session.WindowChange(31, 101); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	_, _ = stdin.Write([]byte("go\n"))
	read("31 101")
	read("WAITING")
	time.Sleep(100 * time.Millisecond)
	_, _ = stdin.Write([]byte{3})
	read("INTERRUPTED")
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxSessionCancellationKillsItsProcessGroup(t *testing.T) {
	f := newTunnel(t)
	client := f.dial(t)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(f.dir, "pid")
	if err := session.Start(fmt.Sprintf("sleep 60 & echo $! > '%s'; wait", pidfile)); err != nil {
		t.Fatal(err)
	}
	var child int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(pidfile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if child > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if child <= 0 {
		t.Fatal("child did not start")
	}
	_ = session.Close()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", child))
		if err != nil || strings.Contains(string(data), ") Z ") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(child, syscall.SIGKILL)
	t.Fatal("child survived SSH channel close")
}

func TestEnrollmentExchangesTicketAndRejectsUnauthorizedControl(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := Enroll(ctx, "http://example.com", "ticket", "test"); err == nil {
		t.Fatal("plaintext enrollment accepted")
	}
}
