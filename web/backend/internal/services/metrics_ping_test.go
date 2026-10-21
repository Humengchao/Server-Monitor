package services

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func requireMetricPing(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the web image runs Linux iputils ping")
	}
	if _, err := exec.LookPath("ping"); err != nil {
		t.Skip("ping is not installed")
	}
}

func TestMetricPingLoopbackCompletesWithoutDefaultIntervalDelay(t *testing.T) {
	requireMetricPing(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	started := time.Now()
	out, err := metricPingCommand(ctx, "127.0.0.1").CombinedOutput()
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("loopback ping failed: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "3 received") && !strings.Contains(string(out), "3 packets received") {
		t.Fatalf("ping did not retain all three observations: %s", out)
	}
	if latency := parsePingAverage(string(out)); latency < 1 {
		t.Fatalf("loopback latency = %d, output: %s", latency, out)
	}
	if elapsed >= 1800*time.Millisecond {
		t.Fatalf("loopback ping occupied a slot for %s; expected less than the old two-second interval", elapsed)
	}
}

func TestMetricPingStopsWithCollectorContext(t *testing.T) {
	requireMetricPing(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan int, 1)
	go func() { finished <- collectPingLatency(ctx, "127.0.0.1") }()
	// Cancel during the three-packet sample, not just before spawning ping.
	time.Sleep(50 * time.Millisecond)
	started := time.Now()
	cancel()
	select {
	case latency := <-finished:
		if latency != 0 {
			t.Fatalf("interrupted ping reported a completed average: %d", latency)
		}
		if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
			t.Fatalf("ping did not promptly observe cancellation: %s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("ping outlived the collector cancellation")
	}
}
