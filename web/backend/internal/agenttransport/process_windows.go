//go:build windows

package agenttransport

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
)

type windowsProcess struct {
	mu           sync.Mutex
	job, console windows.Handle
	input        *os.File
	outputs      []*os.File
	closed       bool
}

func (p *windowsProcess) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	job, console := p.job, p.console
	p.job, p.console = 0, 0
	p.mu.Unlock()
	// Kill the complete job before releasing pipe handles. Children cannot
	// escape the job: the process was assigned while initially suspended.
	if job != 0 {
		_ = windows.CloseHandle(job)
	}
	if p.input != nil {
		_ = p.input.Close()
	}
	for _, output := range p.outputs {
		_ = output.Close()
	}
	if console != 0 {
		windows.ClosePseudoConsole(console)
	}
}

func (p *windowsProcess) Resize(size windowRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.console == 0 {
		return errors.New("terminal is closed")
	}
	return windows.ResizePseudoConsole(p.console, windows.Coord{X: int16(size.Columns), Y: int16(size.Rows)})
}

func (p *windowsProcess) Signal(signal string) error {
	if signal == "INT" {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.closed || p.console == 0 {
			return errors.New("interrupt requires a terminal")
		}
		_, err := p.input.Write([]byte{3})
		return err
	}
	if signal == "TERM" || signal == "KILL" || signal == "HUP" {
		p.Close()
		return nil
	}
	return errors.New("unsupported Windows signal")
}

func (s *SSHServer) startWindowsProcess(channel ssh.Channel, terminal *ptyRequest, overrides map[string]string, command string, shell bool) (*runningProcess, error) {
	p := &windowsProcess{}
	failed := true
	defer func() {
		if failed {
			p.Close()
		}
	}()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	p.job = job
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}

	// Child ends are closed in this parent after CreateProcess. Restrict the
	// inheritance list to exactly its stdin/out/err; no service handles leak.
	var childInput, parentInput, parentOutput, childOutput windows.Handle
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	if err = windows.CreatePipe(&childInput, &parentInput, &sa, 0); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(childInput)
	p.input = os.NewFile(uintptr(parentInput), "agent-stdin")
	if err = windows.SetHandleInformation(parentInput, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, err
	}
	if err = windows.CreatePipe(&parentOutput, &childOutput, &sa, 0); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(childOutput)
	output := os.NewFile(uintptr(parentOutput), "agent-stdout")
	p.outputs = append(p.outputs, output)
	if err = windows.SetHandleInformation(parentOutput, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return nil, err
	}
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return nil, err
	}
	defer attributes.Delete()
	startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{}))}, ProcThreadAttributeList: attributes.List()}
	inherit := false
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	var stderr *os.File
	if terminal != nil {
		// Do not inherit the service's redirected standard handles. ConPTY
		// supplies the console handles during child-process initialization.
		startup.StartupInfo.Flags = windows.STARTF_USESTDHANDLES
		startup.StartupInfo.StdInput = windows.InvalidHandle
		startup.StartupInfo.StdOutput = windows.InvalidHandle
		startup.StartupInfo.StdErr = windows.InvalidHandle
		if err = windows.CreatePseudoConsole(windows.Coord{X: int16(terminal.Columns), Y: int16(terminal.Rows)}, childInput, childOutput, 0, &p.console); err != nil {
			return nil, err
		}
		// Unlike HANDLE_LIST, this attribute takes the opaque HPCON value,
		// not a pointer to it. Pass it as uintptr to the Windows ABI instead
		// of manufacturing a Go pointer from an opaque integer handle.
		update := windows.NewLazySystemDLL("kernel32.dll").NewProc("UpdateProcThreadAttribute")
		ok, _, callErr := update.Call(uintptr(unsafe.Pointer(attributes.List())), 0,
			windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(p.console), unsafe.Sizeof(p.console), 0, 0)
		if ok == 0 {
			return nil, callErr
		}
	} else {
		var parentErr, childErr windows.Handle
		if err = windows.CreatePipe(&parentErr, &childErr, &sa, 0); err != nil {
			return nil, err
		}
		defer windows.CloseHandle(childErr)
		stderr = os.NewFile(uintptr(parentErr), "agent-stderr")
		p.outputs = append(p.outputs, stderr)
		if err = windows.SetHandleInformation(parentErr, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			return nil, err
		}
		handles := []windows.Handle{childInput, childOutput, childErr}
		if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
			return nil, err
		}
		startup.StartupInfo.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
		startup.StartupInfo.ShowWindow = windows.SW_HIDE
		startup.StartupInfo.StdInput, startup.StartupInfo.StdOutput, startup.StartupInfo.StdErr = childInput, childOutput, childErr
		inherit = true
		flags |= windows.CREATE_NO_WINDOW
	}
	executable := s.Shell
	if executable == "" {
		executable = filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	line := syscall.EscapeArg(executable)
	if shell {
		line += " /d /q"
	} else {
		line += " /d /s /c \"" + command + "\""
	}
	lineUTF, err := windows.UTF16PtrFromString(line)
	if err != nil {
		return nil, err
	}
	executableUTF, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return nil, err
	}
	directory, err := windows.UTF16PtrFromString(s.workDir())
	if err != nil {
		return nil, err
	}
	base := s.Environment
	if len(base) == 0 {
		base = os.Environ()
	}
	values := make(map[string]string)
	for _, entry := range base {
		if key, value, ok := strings.Cut(entry, "="); ok && key != "" {
			values[strings.ToUpper(key)] = value
		}
	}
	for key, value := range overrides {
		values[strings.ToUpper(key)] = value
	}
	if terminal != nil {
		values["TERM"] = terminal.Term
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var envText strings.Builder
	for _, key := range keys {
		envText.WriteString(key + "=" + values[key])
		envText.WriteByte(0)
	}
	envText.WriteByte(0)
	env := utf16.Encode([]rune(envText.String()))
	var info windows.ProcessInformation
	if err = windows.CreateProcess(executableUTF, lineUTF, nil, nil, inherit, flags, &env[0], directory, &startup.StartupInfo, &info); err != nil {
		return nil, err
	}
	defer windows.CloseHandle(info.Thread)
	if err = windows.AssignProcessToJobObject(job, info.Process); err != nil {
		_ = windows.TerminateProcess(info.Process, 1)
		_ = windows.CloseHandle(info.Process)
		return nil, err
	}
	if _, err = windows.ResumeThread(info.Thread); err != nil {
		_ = windows.TerminateProcess(info.Process, 1)
		_ = windows.CloseHandle(info.Process)
		return nil, err
	}
	var copies sync.WaitGroup
	copies.Add(1)
	go func() { defer copies.Done(); _, _ = io.Copy(channel, output) }()
	if stderr != nil {
		copies.Add(1)
		go func() { defer copies.Done(); _, _ = io.Copy(channel.Stderr(), stderr) }()
	}
	go func() { _, _ = io.Copy(p.input, channel); _ = p.input.Close() }()
	done := make(chan uint32, 1)
	go func() {
		_, _ = windows.WaitForSingleObject(info.Process, windows.INFINITE)
		status := uint32(1)
		_ = windows.GetExitCodeProcess(info.Process, &status)
		_ = windows.CloseHandle(info.Process)
		// ConPTY remains alive after its client exits; close it while stdout is
		// still being drained, then wait for the final terminal frame.
		if terminal != nil {
			p.mu.Lock()
			console := p.console
			p.console = 0
			p.mu.Unlock()
			if console != 0 {
				windows.ClosePseudoConsole(console)
			}
		}
		copied := make(chan struct{})
		go func() { copies.Wait(); close(copied) }()
		select {
		case <-copied:
		case <-time.After(2 * time.Second):
			for _, output := range p.outputs {
				_ = output.Close()
			}
		}
		done <- status
	}()
	failed = false
	return &runningProcess{native: p, done: done}, nil
}

// The generic Linux branch is compiled but never selected on Windows.
func prepareProcess(*exec.Cmd)                         {}
func startPTY(*exec.Cmd, ptyRequest) (*os.File, error) { return nil, errors.New("use ConPTY") }
func resizePTY(*os.File, windowRequest) error          { return errors.New("use ConPTY") }
func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func signalProcess(*exec.Cmd, *os.File, string) error { return errors.New("use Windows job control") }
