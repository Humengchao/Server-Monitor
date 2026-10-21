package services

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"server-monitor/internal/models"
)

func TestCPUCounterSamplesRejectInvalidDifferences(t *testing.T) {
	first := "cpu 100 0 100 700 100 0 0 0\n"
	for _, second := range []string{"", first, "cpu 10 0 10 70 10 0 0 0\n", "cpu 99 0 99 710 100 0 0 0\n", "cpu 120 0 120 690 100 0 0 0\n", "cpu broken 0 120 710 100 0 0 0\n"} {
		if value := cpuPercentFromSamples(first, second); !math.IsNaN(value) {
			t.Fatalf("invalid CPU counters returned %v: %q", value, second)
		}
	}
	if value := cpuPercentFromSamples(first, "cpu 100 0 100 800 100 0 0 0\n"); value != 0 {
		t.Fatalf("valid zero CPU rejected: %v", value)
	}
}

func TestWindowsCollectionDoesNotTurnInvalidCPUIntoZero(t *testing.T) {
	for _, cpu := range []string{"", "cpu=-3\n", "cpu=101\n", "cpu=NaN\n", "cpu=Infinity\n"} {
		t.Run(strings.TrimSpace(cpu), func(t *testing.T) {
			address, channels, _ := startSessionTestSSHServer(t)
			server := testCacheServer(t, address)
			client, err := newTestCache(t).Get(server)
			if err != nil {
				t.Fatal(err)
			}
			collector := NewCollector(nil, time.Second)
			defer collector.Stop()
			result := make(chan error, 1)
			go func() { _, err := collector.collectWindows(client, server); result <- err }()
			channel, requests, err := awaitSessionChannel(t, channels).Accept()
			if err != nil {
				t.Fatal(err)
			}
			defer channel.Close()
			request := <-requests
			if request.Type != "exec" {
				t.Fatal(request.Type)
			}
			_ = request.Reply(true, nil)
			_, _ = fmt.Fprint(channel, cpu+"memtotal=1024\nmemfree=512\n")
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			_ = channel.Close()
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), "CPU measurement unavailable") {
					t.Fatalf("invalid CPU accepted: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("collector did not finish")
			}
		})
	}
}

func TestCounterResetsAreIndependent(t *testing.T) {
	collector := NewCollector(nil, time.Second)
	defer collector.Stop()
	id := uuid.New()
	start := time.Now()
	collector.applyRates(id, &models.MetricPoint{RecordedAt: start}, 1000, 2000, 3000, 4000)
	reset := &models.MetricPoint{RecordedAt: start.Add(time.Second)}
	collector.applyRates(id, reset, 1200, 10, 5, 4400)
	if reset.NetworkRxBytes != 200 || reset.NetworkTxBytes != 0 || reset.DiskRxBytes != 0 || reset.DiskTxBytes != 400 {
		t.Fatalf("independent reset: %+v", reset)
	}
	next := &models.MetricPoint{RecordedAt: start.Add(2 * time.Second)}
	collector.applyRates(id, next, 1300, 110, 105, 4500)
	if next.NetworkRxBytes != 100 || next.NetworkTxBytes != 100 || next.DiskRxBytes != 100 || next.DiskTxBytes != 100 {
		t.Fatalf("reset did not rebase: %+v", next)
	}
}

func TestWindowsProbeUsesValidFallbackAndRejectsUnknownCPU(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell Windows probe fixture")
	}
	for _, tc := range []struct {
		name     string
		idle     int
		fallback string
		want     string
		failed   bool
	}{
		{"normal", 1075, "17", "cpu=25", false},
		{"negative_delta", 1103, "17", "cpu=17", false},
		{"counter_reset", 500, "17", "cpu=17", false},
		{"true_zero", 1100, "17", "cpu=0", false},
		{"bad_fallback", 1103, "-4", "", true},
		{"missing_fallback", 1103, "$null", "", true},
		{"nan_fallback", 1103, "[double]::NaN", "", true},
		{"infinite_fallback", 1103, "[double]::PositiveInfinity", "", true},
		{"nonnumeric_fallback", 1103, "'unavailable'", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := fmt.Sprintf(`$script:rawCalls=0
function Start-Sleep { param($Milliseconds) }
function Get-Command { param($Name,$ErrorAction) }
function Get-Process { }
function Get-CimInstance {
 param($ClassName,$Filter)
 switch ($ClassName) {
  'Win32_OperatingSystem' { [pscustomobject]@{TotalVisibleMemorySize=1024;FreePhysicalMemory=512;LastBootUpTime=(Get-Date).AddHours(-1)} }
  'Win32_PerfRawData_PerfOS_Processor' { $script:rawCalls++; if($script:rawCalls -eq 1){[pscustomobject]@{Name='_Total';Timestamp_Sys100NS=1000;PercentProcessorTime=1000}}else{[pscustomobject]@{Name='_Total';Timestamp_Sys100NS=1100;PercentProcessorTime=%d}} }
  'Win32_Processor' { [pscustomobject]@{LoadPercentage=%s} }
  'Win32_PerfRawData_Tcpip_NetworkInterface' { [pscustomobject]@{Name='fixture';BytesReceivedPersec=100;BytesSentPersec=100} }
  'Win32_PerfRawData_PerfDisk_PhysicalDisk' { [pscustomobject]@{DiskReadBytesPersec=100;DiskWriteBytesPersec=100} }
  'Win32_LogicalDisk' { [pscustomobject]@{Size=1024;FreeSpace=512} }
  'Win32_PerfFormattedData_PerfOS_System' { [pscustomobject]@{ProcessorQueueLength=0} }
  default { throw 'Unexpected fixture class' }
 }
}
try {
& {
%s
}
exit 0
} catch { [Console]::Error.WriteLine('CPU fixture rejected'); exit 7 }
`, tc.idle, tc.fallback, windowsWebMetricsScript)
			path := filepath.Join(t.TempDir(), "probe.ps1")
			if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path).CombinedOutput()
			if tc.failed {
				if err == nil || strings.Contains(string(out), "cpu=") {
					t.Fatalf("invalid fallback emitted metrics: %s %v", out, err)
				}
			} else if err != nil || !strings.Contains(string(out), tc.want+"\r\n") && !strings.Contains(string(out), tc.want+"\n") {
				t.Fatalf("probe output: %s %v", out, err)
			}
		})
	}
}
