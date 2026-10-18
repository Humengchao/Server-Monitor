//go:build linux

package agenttransport

import (
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

func (s *SSHServer) startWindowsProcess(ssh.Channel, *ptyRequest, map[string]string, string, bool) (*runningProcess, error) {
	return nil, errors.New("Windows process unavailable")
}

func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func startPTY(cmd *exec.Cmd, request ptyRequest) (*os.File, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	file, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(request.Columns), Rows: uint16(request.Rows)})
	if err != nil {
		return nil, err
	}
	// Nonblocking mode lets Close interrupt the output goroutine's Read.
	if err := unix.SetNonblock(int(file.Fd()), true); err != nil {
		_ = file.Close()
		killProcess(cmd)
		_ = cmd.Wait()
		return nil, err
	}
	if err := applyTerminalModes(file, []byte(request.Modes)); err != nil {
		_ = file.Close()
		killProcess(cmd)
		_ = cmd.Wait()
		return nil, err
	}
	return file, nil
}

func applyTerminalModes(file *os.File, modes []byte) error {
	term, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	if err != nil {
		return err
	}
	for len(modes) > 0 {
		op := modes[0]
		modes = modes[1:]
		if op == 0 || op >= 160 {
			break
		}
		if len(modes) < 4 {
			return errors.New("invalid terminal modes")
		}
		value := binary.BigEndian.Uint32(modes[:4])
		modes = modes[4:]
		set := func(flags *uint32, bit uint32) {
			if value != 0 {
				*flags |= bit
			} else {
				*flags &^= bit
			}
		}
		switch op {
		case ssh.ECHO:
			set(&term.Lflag, unix.ECHO)
		case ssh.ICANON:
			set(&term.Lflag, unix.ICANON)
		case ssh.ISIG:
			set(&term.Lflag, unix.ISIG)
		case ssh.IEXTEN:
			set(&term.Lflag, unix.IEXTEN)
		case ssh.OPOST:
			set(&term.Oflag, unix.OPOST)
		case ssh.ONLCR:
			set(&term.Oflag, unix.ONLCR)
		case ssh.ICRNL:
			set(&term.Iflag, unix.ICRNL)
		case ssh.IXON:
			set(&term.Iflag, unix.IXON)
		case ssh.VINTR:
			term.Cc[unix.VINTR] = uint8(value)
		case ssh.VEOF:
			term.Cc[unix.VEOF] = uint8(value)
		case ssh.VERASE:
			term.Cc[unix.VERASE] = uint8(value)
		}
	}
	return unix.IoctlSetTermios(int(file.Fd()), unix.TCSETS, term)
}

func resizePTY(file *os.File, size windowRequest) error {
	return pty.Setsize(file, &pty.Winsize{Cols: uint16(size.Columns), Rows: uint16(size.Rows)})
}

func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func signalProcess(cmd *exec.Cmd, terminal *os.File, name string) error {
	signals := map[string]syscall.Signal{"HUP": syscall.SIGHUP, "INT": syscall.SIGINT, "QUIT": syscall.SIGQUIT, "KILL": syscall.SIGKILL, "TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1, "USR2": syscall.SIGUSR2}
	sig, ok := signals[name]
	if !ok || cmd.Process == nil {
		return errors.New("unsupported signal")
	}
	group := cmd.Process.Pid
	if terminal != nil {
		if foreground, err := unix.IoctlGetInt(int(terminal.Fd()), unix.TIOCGPGRP); err == nil && foreground > 0 {
			group = foreground
		}
	}
	return syscall.Kill(-group, sig)
}
