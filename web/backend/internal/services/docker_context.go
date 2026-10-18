package services

import (
	"context"
	"errors"

	"golang.org/x/crypto/ssh"
)

var errDockerOutputLimit = errors.New("docker output limit exceeded")

func RunDockerContext(ctx context.Context, client *ssh.Client, arguments string, limit int) (string, error) {
	run := func(command string) (string, error) {
		output, truncated, err := runCmdContext(ctx, client, command, limit)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if truncated {
			return "", errDockerOutputLimit
		}
		return output, err
	}
	output, err := run("docker " + arguments)
	if err != nil && ctx.Err() == nil && !errors.Is(err, errDockerOutputLimit) {
		return run("sudo -n docker " + arguments)
	}
	return output, err
}
