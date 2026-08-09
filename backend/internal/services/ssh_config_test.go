package services

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSSHAuthMethodsRequiresCredential(t *testing.T) {
	methods, err := buildSSHAuthMethods("", "")
	if err == nil {
		t.Fatal("buildSSHAuthMethods() error = nil, want no-auth error")
	}
	if methods != nil {
		t.Fatalf("buildSSHAuthMethods() methods = %v, want nil", methods)
	}
	if !strings.Contains(err.Error(), "no auth method") {
		t.Fatalf("buildSSHAuthMethods() error = %q, want no auth method", err)
	}
}

func TestBuildSSHAuthMethodsPasswordAddsOnePasswordMethod(t *testing.T) {
	methods, err := buildSSHAuthMethods("secret", "")
	if err != nil {
		t.Fatalf("buildSSHAuthMethods() unexpected error: %v", err)
	}
	if got, want := len(methods), 1; got != want {
		t.Fatalf("buildSSHAuthMethods() method count = %d, want %d", got, want)
	}
}

func TestBuildSSHAuthMethodsPasswordTakesPrecedenceOverInvalidKey(t *testing.T) {
	methods, err := buildSSHAuthMethods("secret", "not a private key")
	if err != nil {
		t.Fatalf("buildSSHAuthMethods() returned key error despite password precedence: %v", err)
	}
	if got, want := len(methods), 1; got != want {
		t.Fatalf("buildSSHAuthMethods() fallback method count = %d, want %d", got, want)
	}
}

func TestBuildSSHAuthMethodsInvalidKeyWithoutPasswordFails(t *testing.T) {
	methods, err := buildSSHAuthMethods("", "not a private key")
	if err == nil {
		t.Fatal("buildSSHAuthMethods() error = nil, want invalid-key error")
	}
	if methods != nil {
		t.Fatalf("buildSSHAuthMethods() methods = %v, want nil", methods)
	}
}

func TestBuildSSHClientConfigRejectsInvalidHostKey(t *testing.T) {
	config, err := buildSSHClientConfig("root", "secret", "", "not a host key", time.Second)
	if err == nil {
		t.Fatal("buildSSHClientConfig() error = nil, want invalid-host-key error")
	}
	if config != nil {
		t.Fatalf("buildSSHClientConfig() config = %#v, want nil", config)
	}
	if !strings.Contains(err.Error(), "parse host key") {
		t.Fatalf("buildSSHClientConfig() error = %q, want parse host key error", err)
	}
}
