package config

import "testing"

func TestExpiryRenewalConfiguration(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("JWT_SECRET", "q4N8r7W2a9V6z1L5m0H3k8Y7p2C4s9T6")
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("POLL_INTERVAL", "3")
	t.Setenv("ALERT_INTERVAL", "30")
	t.Setenv("ALLOW_REGISTRATION", "true")
	t.Setenv("TRUSTED_PROXIES", "none")
	t.Setenv("AUTO_RENEW_EXPIRY", "")
	t.Setenv("EXPIRY_TIMEZONE", "")
	cfg, err := Load()
	if err != nil || !cfg.AutoRenewExpiry || cfg.ExpiryTimezone.String() != "Asia/Shanghai" {
		t.Fatalf("default renewal config: %#v %v", cfg, err)
	}
	t.Setenv("AUTO_RENEW_EXPIRY", "false")
	t.Setenv("EXPIRY_TIMEZONE", "UTC")
	cfg, err = Load()
	if err != nil || cfg.AutoRenewExpiry || cfg.ExpiryTimezone.String() != "UTC" {
		t.Fatalf("explicit renewal config: %#v %v", cfg, err)
	}
	t.Setenv("EXPIRY_TIMEZONE", "not/a/timezone")
	if _, err = Load(); err == nil {
		t.Fatal("invalid calendar timezone accepted")
	}
}

func TestJWTSecretRejectsUnsafeConfiguration(t *testing.T) {
	for _, secret := range []string{"", "abc", "change-me-to-a-random-string-at-least-32-bytes", "0123456789abcdef0123456789abcdef", "                                ", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", " q4N8r7W2a9V6z1L5m0H3k8Y7p2C4s9T6"} {
		if validateJWTSecret(secret) == nil {
			t.Fatalf("unsafe JWT configuration accepted (length %d)", len(secret))
		}
	}
	if err := validateJWTSecret("q4N8r7W2a9V6z1L5m0H3k8Y7p2C4s9T6"); err != nil {
		t.Fatal(err)
	}
}

func TestSSHHostKeyMigrationPolicyDefaultsToStrict(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://example.invalid/test")
	t.Setenv("JWT_SECRET", "q4N8r7W2a9V6z1L5m0H3k8Y7p2C4s9T6")
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("SSH_ALLOW_UNVERIFIED_HOST_KEYS", "")
	cfg, err := Load()
	if err != nil || cfg.SSHAllowUnverifiedHostKeys {
		t.Fatalf("strict default: %v %v", cfg, err)
	}
	t.Setenv("SSH_ALLOW_UNVERIFIED_HOST_KEYS", "true")
	cfg, err = Load()
	if err != nil || !cfg.SSHAllowUnverifiedHostKeys {
		t.Fatalf("explicit migration opt-in: %v %v", cfg, err)
	}
	t.Setenv("SSH_ALLOW_UNVERIFIED_HOST_KEYS", "treu")
	if _, err = Load(); err == nil {
		t.Fatal("misspelled host-key exception accepted")
	}
}

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
