//go:build !windows

package main

import "errors"

func runService(string) error { return errors.New("use systemd with the run command on Linux") }
