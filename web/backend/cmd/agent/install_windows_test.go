//go:build windows

package main

import (
	"context"
	"os"
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
	for _, modulePathMissing := range []bool{false, true} {
		name := "inherited_module_path"
		if modulePathMissing {
			name = "without_system_modules"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			arguments := []string{"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass"}
			root := t.TempDir()
			if modulePathMissing {
				// Windows PowerShell may normalize an inherited PSModulePath on
				// startup or find cached module paths. Disable both discovery
				// mechanisms to prove the fixture imports its required system
				// modules explicitly before installing its safe mocks.
				arguments = append(arguments, "-Command", `$env:PSModulePath=$env:AGENT_FIXTURE_ROOT; $PSModuleAutoLoadingPreference='None'; & $env:AGENT_FIXTURE_SCRIPT -FixtureRoot $env:AGENT_FIXTURE_ROOT`)
			} else {
				arguments = append(arguments, "-File", script, "-FixtureRoot", root)
			}
			command := exec.CommandContext(ctx, "powershell", arguments...)
			command.Env = append(os.Environ(), "AGENT_FIXTURE_ROOT="+root, "AGENT_FIXTURE_SCRIPT="+script)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("isolated installer fixture: %v\n%s", err, output)
			}
		})
	}
}
