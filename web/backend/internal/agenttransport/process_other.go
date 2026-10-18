//go:build !linux && !windows

package agenttransport

import (
	"errors"
	"golang.org/x/crypto/ssh"
	"os"
	"os/exec"
)

func (s *SSHServer) startWindowsProcess(ssh.Channel, *ptyRequest, map[string]string, string, bool) (*runningProcess, error) {
	return nil, errors.New("Windows process unavailable")
}

func prepareProcess(*exec.Cmd) {}
func startPTY(*exec.Cmd, ptyRequest) (*os.File, error) {
	return nil, errors.New("agent PTY requires Linux")
}
func resizePTY(*os.File, windowRequest) error { return errors.New("agent PTY requires Linux") }
func killProcess(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
func signalProcess(*exec.Cmd, *os.File, string) error {
	return errors.New("agent signals require Linux")
}
