//go:build windows

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsInstallerRetainsCommittedEnrollmentDuringRollback(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "..", "agent-assets", "install_windows_test.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-FixtureRoot", t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("isolated installer fixture: %v\n%s", err, output)
	}
}
