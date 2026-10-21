package services

import (
	"compress/flate"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestSharedWindowsProbeDeliveryRoundTrip(t *testing.T) {
	if len(windowsMetricsCommand) >= 8191 {
		t.Fatalf("Windows command is too long: %d", len(windowsMetricsCommand))
	}
	encoded := strings.TrimPrefix(windowsMetricsCommand, "powershell -NoProfile -NonInteractive -EncodedCommand ")
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[i*2]) | uint16(raw[i*2+1])<<8
	}
	stub := string(utf16.Decode(units))
	parts := strings.Split(stub, "'")
	if len(parts) < 3 {
		t.Fatal("missing payload")
	}
	compressed, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	reader := flate.NewReader(strings.NewReader(string(compressed)))
	defer reader.Close()
	script, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(script) != windowsWebMetricsScript {
		t.Fatal("script changed in delivery")
	}
	if !strings.Contains(string(script), "$raw2 = Get-CimInstance Win32_PerfRawData_PerfOS_Processor") || !strings.Contains(string(script), "memtotal=") {
		t.Fatal("missing canonical probes")
	}
	for _, expensive := range []string{"docker info", "Get-Process", "Win32_NetworkAdapterConfiguration", "ident=", "proc="} {
		if strings.Contains(string(script), expensive) {
			t.Fatalf("web probe includes %s", expensive)
		}
	}
}

func TestSharedLinuxProbeSections(t *testing.T) {
	sections := strings.Split(linuxMetricsCommand, "; echo "+linuxSectionSeparator+"; ")
	if len(sections) != linuxSectionCount {
		t.Fatalf("section count=%d", len(sections))
	}
	if sections[linuxSectionMemInfo] != "cat /proc/meminfo" || sections[linuxSectionDiskUsage] != sharedProbeValues["web-disk-usage"] {
		t.Fatal("probe contract order changed")
	}
	fixture, err := os.ReadFile("../../../../shared/fixtures/linux/proc-meminfo.txt")
	if err != nil {
		t.Fatal(err)
	}
	used, total := parseMemInfo(string(fixture))
	if used != 11600000*1024 || total != 32900000*1024 {
		t.Fatalf("shared memory fixture=%d/%d", used, total)
	}
}
