package agenttransport

import (
	"context"
	"errors"
)

type expectedHostKeyContextKey struct{}
type expectedRuntimeContextKey struct{}
type expectedRuntime struct{ username, platform string }

var ErrAgentIdentityChanged = errors.New("the enrolled agent identity changed; review a new operation before executing")

// WithExpectedHostKey pins a dial to the identity approved for a long-lived
// operation. Unpinned interactive dials continue following current enrollment.
// Even an empty/invalid explicit pin fails closed rather than becoming optional.
func WithExpectedHostKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, expectedHostKeyContextKey{}, key)
}

// WithExpectedIdentity also binds the operating system and execution account.
// A preserved key can move from a foreground user to a privileged service.
func WithExpectedIdentity(ctx context.Context, key, username, platform string) context.Context {
	ctx = context.WithValue(ctx, expectedRuntimeContextKey{}, expectedRuntime{username, platform})
	return WithExpectedHostKey(ctx, key)
}
