package agenttransport

import (
	"context"
	"errors"
)

type expectedHostKeyContextKey struct{}

var ErrAgentIdentityChanged = errors.New("the enrolled agent identity changed; review a new operation before executing")

// WithExpectedHostKey pins a dial to the identity approved for a long-lived
// operation. Unpinned interactive dials continue following current enrollment.
// Even an empty/invalid explicit pin fails closed rather than becoming optional.
func WithExpectedHostKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, expectedHostKeyContextKey{}, key)
}
