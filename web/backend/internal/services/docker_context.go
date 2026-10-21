package services

import (
	"context"
	"errors"
	"strings"

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
	if ctx.Err() == nil && dockerPermissionDenied(output, err) {
		return run("sudo -n docker " + arguments)
	}
	return output, err
}

func dockerPermissionDenied(output string, err error) bool {
	var exit *ssh.ExitError
	return errors.As(err, &exit) && strings.Contains(strings.ToLower(output), "permission denied") &&
		(strings.Contains(output, "docker.sock") || strings.Contains(strings.ToLower(output), "docker daemon socket"))
}

// Determine privilege using a harmless probe before executing a mutation.
// Once the mutation starts, any error may hide a side effect: never retry it.
func RunDockerMutationContext(ctx context.Context, client *ssh.Client, arguments string) error {
	command := "docker "
	output, truncated, err := runCmdContext(ctx, client, command+"info --format '{{.ServerVersion}}'", 4096)
	if truncated {
		return errDockerOutputLimit
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if dockerPermissionDenied(output, err) {
		command = "sudo -n docker "
		_, _, err = runCmdContext(ctx, client, command+"info --format '{{.ServerVersion}}'", 4096)
	}
	if err != nil {
		return err
	}
	_, truncated, err = runCmdContext(ctx, client, command+arguments, 64*1024)
	if truncated {
		return errDockerOutputLimit
	}
	return err
}
