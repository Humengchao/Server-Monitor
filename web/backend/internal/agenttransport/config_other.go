//go:build !windows

package agenttransport

func protectConfig(string) error { return nil }
