//go:build windows

package agenttransport

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
)

func encodedPowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[i*2:], unit)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data)
}

func TestWindowsExecExitStatusAndSeparateStderr(t *testing.T) {
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
	err = session.Run("echo HELLO& echo ERROR 1>&2& exit /b 7")
	exit, ok := err.(*ssh.ExitError)
	if !ok || exit.ExitStatus() != 7 || !strings.Contains(stdout.String(), "HELLO") || !strings.Contains(stderr.String(), "ERROR") {
		t.Fatalf("%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
}

type capturedOutput struct {
	mu    sync.Mutex
	value strings.Builder
}

func (c *capturedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value.Write(p)
}
func (c *capturedOutput) String() string { c.mu.Lock(); defer c.mu.Unlock(); return c.value.String() }
func waitOutput(t *testing.T, output *capturedOutput, want string) {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		if strings.Contains(output.String(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing %q in terminal output %q", want, output.String())
}

func TestWindowsConPTYResizeUnicodeAndControlC(t *testing.T) {
	f := newTunnel(t)
	client := f.dial(t)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if err := session.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output := &capturedOutput{}
	session.Stdout = output
	session.Stderr = output
	// ReadLine pauses before the size check, so the resize has to reach ConPTY.
	script := `[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); Write-Output 'READY'; [Console]::ReadLine() | Out-Null; Write-Output ('SIZE='+[Console]::WindowWidth+'x'+[Console]::WindowHeight); Write-Output '中文'; try { Write-Output 'WAITING'; Start-Sleep -Seconds 60 } finally { Write-Output 'INTERRUPTED' }`
	if err := session.Start(encodedPowerShell(script)); err != nil {
		t.Fatal(err)
	}
	waitOutput(t, output, "READY")
	if err := session.WindowChange(31, 101); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	_, _ = io.WriteString(stdin, "\r")
	waitOutput(t, output, "SIZE=101x31")
	waitOutput(t, output, "中文")
	waitOutput(t, output, "WAITING")
	time.Sleep(100 * time.Millisecond)
	_, _ = stdin.Write([]byte{3})
	finished := make(chan error, 1)
	go func() { finished <- session.Wait() }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl+C did not interrupt the running program")
	}
}

func TestWindowsCanceledChannelKillsDescendantJob(t *testing.T) {
	f := newTunnel(t)
	client := f.dial(t)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	pidfile := filepath.Join(f.dir, "child.pid")
	script := fmt.Sprintf(`$child=Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -ArgumentList '-NoProfile','-NonInteractive','-Command','Start-Sleep -Seconds 60' -WindowStyle Hidden -PassThru; [IO.File]::WriteAllText('%s',[string]$child.Id); Wait-Process -Id $child.Id`, strings.ReplaceAll(pidfile, "'", "''"))
	if err := session.Start(encodedPowerShell(script)); err != nil {
		t.Fatal(err)
	}
	var child int
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		data, _ := os.ReadFile(pidfile)
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if child > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child <= 0 {
		_ = session.Close()
		t.Fatal("child did not start")
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(child))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	_ = session.Close()
	state, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil || state != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(handle, 1)
		t.Fatalf("child survived session cancellation: state=%d err=%v", state, err)
	}
}
