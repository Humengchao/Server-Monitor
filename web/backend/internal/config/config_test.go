package config

import "testing"

func TestLoadBoolDefault(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		set      bool
		fallback bool
		want     bool
		wantErr  bool
	}{
		{name: "unset keeps fallback true", set: false, fallback: true, want: true},
		{name: "unset keeps fallback false", set: false, fallback: false, want: false},
		{name: "true", value: "true", set: true, fallback: false, want: true},
		{name: "false", value: "false", set: true, fallback: true, want: false},
		{name: "1 means on", value: "1", set: true, fallback: false, want: true},
		{name: "0 means off", value: "0", set: true, fallback: true, want: false},
		{name: "case insensitive", value: "False", set: true, fallback: true, want: false},
		{name: "surrounding spaces trimmed", value: " false ", set: true, fallback: true, want: false},
		{name: "garbage is an error", value: "nope", set: true, fallback: true, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const key = "TEST_LOAD_BOOL_DEFAULT"
			if tc.set {
				t.Setenv(key, tc.value)
			}
			got, err := loadBoolDefault(key, tc.fallback)
			if (err != nil) != tc.wantErr {
				t.Fatalf("loadBoolDefault(%q) err = %v, wantErr %v", tc.value, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("loadBoolDefault(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
