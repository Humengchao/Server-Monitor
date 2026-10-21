package services

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"server-monitor/internal/models"
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

func TestSSHHostTrustBeforePasswordAuthentication(t *testing.T) {
	ConfigureSSHHostKeyPolicy(false)
	newSigner := func() ssh.Signer {
		_, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		signer, err := ssh.NewSignerFromKey(private)
		if err != nil {
			t.Fatal(err)
		}
		return signer
	}
	signer, different := newSigner(), newSigner()
	for _, test := range []struct {
		name, pin     string
		authenticated bool
	}{
		{"missing", "", false},
		{"mismatch", string(ssh.MarshalAuthorizedKey(different.PublicKey())), false},
		{"trusted", string(ssh.MarshalAuthorizedKey(signer.PublicKey())), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var passwordObserved atomic.Bool
			serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
				passwordObserved.Store(true)
				return nil, nil
			}}
			serverConfig.AddHostKey(signer)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				sshConn, _, _, err := ssh.NewServerConn(conn, serverConfig)
				if err == nil {
					sshConn.Close()
				}
			}()
			host, portText, _ := net.SplitHostPort(listener.Addr().String())
			port, _ := strconv.Atoi(portText)
			client, _, err := DialServer(context.Background(), &models.Server{ConnectionMode: "ssh", Host: host, Port: port, SSHUsername: "fixture", SSHPassword: "test-only-password", SSHHostKey: test.pin})
			if client != nil {
				client.Close()
			}
			if test.authenticated != (err == nil) {
				t.Fatalf("dial result %v", err)
			}
			listener.Close()
			<-done
			if passwordObserved.Load() != test.authenticated {
				t.Fatal("host trust did not gate password authentication")
			}
		})
	}
}

func TestSSHMigrationExceptionDoesNotOverridePin(t *testing.T) {
	ConfigureSSHHostKeyPolicy(true)
	defer ConfigureSSHHostKeyPolicy(false)
	if _, err := buildHostKeyCallback(""); err != nil {
		t.Fatal(err)
	}
	if _, err := buildHostKeyCallback("invalid pinned key"); err == nil {
		t.Fatal("migration exception ignored a supplied invalid pin")
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
