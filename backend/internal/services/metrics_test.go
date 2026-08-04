package services

import "testing"

func TestParsePingAverage(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   int
	}{
		{"iputils", "rtt min/avg/max/mdev = 31.204/32.684/34.102/1.185 ms", 33},
		{"busybox", "round-trip min/avg/max = 0.451/0.728/1.005 ms", 1},
		{"unavailable", "3 packets transmitted, 0 received, 100% packet loss", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parsePingAverage(test.output); got != test.want {
				t.Fatalf("parsePingAverage() = %d, want %d", got, test.want)
			}
		})
	}
}
