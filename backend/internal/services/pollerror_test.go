package services

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestClassifyPollError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want PollErrorKind
	}{
		{"nil", nil, ""},
		{
			// The real message x/crypto/ssh produces for a rejected password.
			"rejected password",
			errors.New("ssh handshake: ssh: handshake failed: ssh: unable to authenticate, attempted methods [none password], no supported methods remain"),
			PollErrorAuth,
		},
		{
			// A host-key mismatch is also a handshake failure, so it has to be
			// tested before the auth case or it would be reported as a bad
			// password — sending the reader to change a credential that is fine.
			"host key mismatch",
			errors.New("ssh handshake: ssh: handshake failed: knownhosts: key mismatch"),
			PollErrorHostKey,
		},
		{"refused", errors.New("ssh dial: dial tcp 10.0.0.1:22: connect: connection refused"), PollErrorUnreachable},
		{"no route", errors.New("ssh dial: dial tcp 10.0.0.1:22: connect: no route to host"), PollErrorUnreachable},
		{"dns", errors.New("ssh dial: dial tcp: lookup nope.invalid: no such host"), PollErrorUnreachable},
		{"reset", errors.New("ssh dial: read tcp 10.0.0.1:22: connection reset by peer"), PollErrorUnreachable},
		{
			// "i/o timeout" is a dial error too, but a firewall dropping packets
			// needs a different fix from one refusing them, so it keeps its own
			// kind and must win over the unreachable cases.
			"dial timeout",
			errors.New("ssh dial: dial tcp 10.0.0.1:22: i/o timeout"),
			PollErrorTimeout,
		},
		{"context deadline", errors.New("run probe: context deadline exceeded"), PollErrorTimeout},
		{"probe failed", errors.New("ssh session: exited with status 127"), PollErrorCommand},
		{"storage", fmt.Errorf("save metric: %w", errors.New("driver: bad connection")), PollErrorStorage},
		{"unknown", errors.New("something nobody has seen before"), PollErrorOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyPollError(tc.err); got != tc.want {
				t.Errorf("ClassifyPollError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestTrimPollErrorDetail(t *testing.T) {
	if got := TrimPollErrorDetail(nil); got != "" {
		t.Errorf("nil error = %q, want empty", got)
	}

	// Newlines are collapsed: the value is rendered inline.
	multi := errors.New("ssh handshake:\n  failed\n\tbadly")
	if got := TrimPollErrorDetail(multi); got != "ssh handshake: failed badly" {
		t.Errorf("collapsed = %q", got)
	}

	long := errors.New(strings.Repeat("x", pollErrorDetailMax+120))
	got := TrimPollErrorDetail(long)
	if len(got) > pollErrorDetailMax+4 {
		t.Errorf("length %d exceeds the cap plus ellipsis", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated value should end with an ellipsis, got %q", got[len(got)-8:])
	}

	// A cut must not split a multi-byte character, or the column stores
	// invalid UTF-8 and Postgres rejects the write.
	cjk := errors.New(strings.Repeat("认证失败", 200))
	trimmed := TrimPollErrorDetail(cjk)
	for i, r := range trimmed {
		if r == '�' {
			t.Fatalf("replacement character at byte %d — cut mid-rune", i)
		}
	}
}
