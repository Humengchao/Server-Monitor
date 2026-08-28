package handlers

import "testing"

func TestNormalizeAlertRule(t *testing.T) {
	tests := []struct {
		name           string
		req            AlertRuleRequest
		wantErr        bool
		wantComparator string
		wantDuration   int
		wantThreshold  float64
	}{
		{
			name:           "fills in the default comparator and duration",
			req:            AlertRuleRequest{Name: " High CPU ", Metric: "cpu", Threshold: 90},
			wantComparator: ">",
			wantDuration:   300,
			wantThreshold:  90,
		},
		{
			name:           "uppercase metric names are accepted",
			req:            AlertRuleRequest{Name: "Disk", Metric: "DISK", Threshold: 85, Comparator: ">", Duration: 600},
			wantComparator: ">",
			wantDuration:   600,
			wantThreshold:  85,
		},
		{
			name:           "offline rules ignore the threshold",
			req:            AlertRuleRequest{Name: "Down", Metric: "offline", Threshold: 42, Comparator: "<", Duration: 300},
			wantComparator: ">",
			wantDuration:   300,
			wantThreshold:  0,
		},
		{
			name:    "blank name is rejected",
			req:     AlertRuleRequest{Name: "   ", Metric: "cpu", Threshold: 90},
			wantErr: true,
		},
		{
			name:    "unknown metric is rejected",
			req:     AlertRuleRequest{Name: "Weird", Metric: "entropy", Threshold: 1},
			wantErr: true,
		},
		{
			name:    "unknown comparator is rejected",
			req:     AlertRuleRequest{Name: "CPU", Metric: "cpu", Comparator: ">=", Threshold: 90},
			wantErr: true,
		},
		{
			name:    "percent metrics are capped at 100",
			req:     AlertRuleRequest{Name: "CPU", Metric: "cpu", Threshold: 120},
			wantErr: true,
		},
		{
			name:    "negative thresholds are rejected",
			req:     AlertRuleRequest{Name: "Latency", Metric: "latency", Threshold: -5},
			wantErr: true,
		},
		{
			name:    "sub-minimum durations are rejected",
			req:     AlertRuleRequest{Name: "CPU", Metric: "cpu", Threshold: 90, Duration: 5},
			wantErr: true,
		},
		{
			name:    "durations beyond a day are rejected",
			req:     AlertRuleRequest{Name: "CPU", Metric: "cpu", Threshold: 90, Duration: 90000},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			err := normalizeAlertRule(&req)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if req.Comparator != tc.wantComparator {
				t.Errorf("comparator = %q, want %q", req.Comparator, tc.wantComparator)
			}
			if req.Duration != tc.wantDuration {
				t.Errorf("duration = %d, want %d", req.Duration, tc.wantDuration)
			}
			if req.Threshold != tc.wantThreshold {
				t.Errorf("threshold = %v, want %v", req.Threshold, tc.wantThreshold)
			}
			if req.Name != "High CPU" && tc.name == "fills in the default comparator and duration" {
				t.Errorf("name = %q, want it trimmed", req.Name)
			}
		})
	}
}
