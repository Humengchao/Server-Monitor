package services

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

var allowUnverifiedSSHHostKeys atomic.Bool

// ConfigureSSHHostKeyPolicy is set once at startup. The explicit migration
// exception never overrides a supplied pin, including a pin mismatch.
func ConfigureSSHHostKeyPolicy(allowUnverified bool) {
	allowUnverifiedSSHHostKeys.Store(allowUnverified)
}

func ValidateSSHHostKey(hostKey string) error {
	_, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil || strings.TrimSpace(string(rest)) != "" {
		return fmt.Errorf("SSH host key must contain one trusted authorized-key public key")
	}
	return nil
}

func buildSSHAuthMethods(password, privateKey string) ([]ssh.AuthMethod, error) {
	// Try exactly one method per connection. Repeated monitoring with a stale
	// first method followed by a valid fallback can still accumulate failures
	// in fail2ban even though every poll eventually succeeds.
	if password != "" {
		return []ssh.AuthMethod{ssh.Password(password)}, nil
	}
	if strings.TrimSpace(privateKey) != "" {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			return nil, fmt.Errorf("parse key: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	return nil, fmt.Errorf("no auth method")
}

func buildHostKeyCallback(hostKey string) (ssh.HostKeyCallback, error) {
	if strings.TrimSpace(hostKey) == "" {
		if allowUnverifiedSSHHostKeys.Load() {
			return ssh.InsecureIgnoreHostKey(), nil
		}
		return nil, fmt.Errorf("SSH host key is required; obtain the public key through a trusted independent channel and pin it before connecting")
	}
	if err := ValidateSSHHostKey(hostKey); err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(hostKey))
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	return ssh.FixedHostKey(parsedKey), nil
}

func buildSSHClientConfig(username, password, privateKey, hostKey string, timeout time.Duration) (*ssh.ClientConfig, error) {
	auth, err := buildSSHAuthMethods(password, privateKey)
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := buildHostKeyCallback(hostKey)
	if err != nil {
		return nil, err
	}
	return &ssh.ClientConfig{
		User:            username,
		Auth:            auth,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}, nil
}
