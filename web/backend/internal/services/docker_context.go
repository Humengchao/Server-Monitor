package services

import (
	"context"
	"errors"
	"golang.org/x/crypto/ssh"
)

func RunDockerContext(ctx context.Context, client *ssh.Client, arguments string, limit int) (string, error) {
	run := func(command string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		session, err := client.NewSession()
		if err != nil {
			return "", err
		}
		defer session.Close()
		writer := &syncLimitedWriter{limit: limit}
		session.Stdout = writer
		session.Stderr = writer
		done := make(chan error, 1)
		go func() { done <- session.Run(command) }()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case err = <-done:
			output, truncated := writer.result()
			if truncated {
				return "", errors.New("docker output limit exceeded")
			}
			return output, err
		}
	}
	output, err := run("docker " + arguments)
	if err != nil && ctx.Err() == nil {
		return run("sudo -n docker " + arguments)
	}
	return output, err
}
