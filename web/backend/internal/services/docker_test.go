package services

import "testing"

func TestParseDockerSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"0B", 0},
		{"1.5KiB", 1536},
		{"2MB", 2_000_000},
		{"1.5 GiB", 1_610_612_736},
		{"12.3kB (virtual 80MB)", 12_300},
		{"--", 0},
		{"not-a-size", 0},
	}
	for _, tt := range tests {
		if got := ParseDockerSize(tt.input); got != tt.want {
			t.Errorf("ParseDockerSize(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseDockerStats(t *testing.T) {
	out := `{"ID":"abc123","CPUPerc":"12.5%","MemUsage":"128MiB / 1GiB","MemPerc":"12.50%","BlockIO":"2.5MB / 1KiB"}
{"Container":"def456","CPUPerc":"0.00%","MemUsage":"0B / 0B","MemPerc":"0.00%"}
warning from daemon`
	got := ParseDockerStats(out)
	first, ok := got["abc123"]
	if !ok {
		t.Fatal("first stats row was not parsed")
	}
	if first.CPUPercent != 12.5 || first.MemoryUsage != 128*1024*1024 || first.MemoryLimit != 1024*1024*1024 || first.MemoryPercent != 12.5 || first.DiskReadBytes != 2_500_000 || first.DiskWriteBytes != 1024 || !first.BlockIOAvailable || !first.StatsAvailable {
		t.Fatalf("unexpected first stats row: %+v", first)
	}
	second, ok := got["def456"]
	if !ok || !second.StatsAvailable {
		t.Fatalf("second stats row was not parsed: %+v", got)
	}
}

func TestParseDockerBlockIOZeroIsAvailable(t *testing.T) {
	got := ParseDockerStats(`{"ID":"zero","CPUPerc":"0%","MemUsage":"0B / 0B","BlockIO":"0B / 0B"}`)
	if !got["zero"].BlockIOAvailable || got["zero"].DiskReadBytes != 0 || got["zero"].DiskWriteBytes != 0 {
		t.Fatalf("zero block I/O should remain available: %+v", got["zero"])
	}
}

func TestMergeDockerDiskUsage(t *testing.T) {
	stats := ParseDockerStats(`{"ID":"abc123","CPUPerc":"1%"}`)
	MergeDockerDiskUsage(stats, "abc123", "2.5MB (virtual 40MB)")
	if got := stats["abc123"].DiskUsage; got != 2_500_000 {
		t.Fatalf("DiskUsage = %d, want 2500000", got)
	}
}

func TestDockerSizesFromPS(t *testing.T) {
	writable, virtual, ok := DockerSizesFromPS("12.3MB (virtual 80MB)")
	if !ok || writable != 12_300_000 || virtual != 80_000_000 {
		t.Fatalf("DockerSizesFromPS() = %d, %d, %v", writable, virtual, ok)
	}
	if writable, virtual, ok = DockerSizesFromPS("0B (virtual 0B)"); !ok || writable != 0 || virtual != 0 {
		t.Fatalf("zero DockerSizesFromPS() = %d, %d, %v", writable, virtual, ok)
	}
	if _, _, ok = DockerSizesFromPS("not-a-size"); ok {
		t.Fatal("malformed size should not be marked available")
	}
}
