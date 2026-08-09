package services

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

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
		return ssh.InsecureIgnoreHostKey(), nil
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
