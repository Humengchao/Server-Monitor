package services

import (
	"strings"
	"unicode/utf8"
)

// PollErrorKind classifies why a poll failed, in the terms an operator would
// use to fix it. The raw error is kept alongside for whoever is debugging, but
// the kind is what the UI translates and acts on: "wrong password" and "host
// unreachable" want completely different next steps, and a card that only says
// "offline" makes the reader guess between them.
type PollErrorKind string

const (
	PollErrorAuth        PollErrorKind = "auth"        // credentials rejected
	PollErrorHostKey     PollErrorKind = "host_key"    // pinned host key mismatch
	PollErrorUnreachable PollErrorKind = "unreachable" // no route, refused, DNS
	PollErrorTimeout     PollErrorKind = "timeout"     // dial or command timed out
	PollErrorCommand     PollErrorKind = "command"     // connected, but the probe failed
	PollErrorStorage     PollErrorKind = "storage"     // the sample could not be persisted
	PollErrorOther       PollErrorKind = "other"
)

// ClassifyPollError maps a collector error onto a PollErrorKind.
//
// It matches on message text because that is all golang.org/x/crypto/ssh and
// net give us: the handshake failure is a plain error, not a typed one. Order
// matters — an authentication failure also mentions "handshake", and a host-key
// mismatch is a handshake failure too, so the more specific tests come first.
func ClassifyPollError(err error) PollErrorKind {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "host key"), strings.Contains(message, "knownhosts"):
		return PollErrorHostKey
	case isSSHAuthenticationFailure(err):
		return PollErrorAuth
	case strings.Contains(message, "save metric"), strings.Contains(message, "sql"):
		return PollErrorStorage
	// Checked before the generic network cases: a timeout reads as
	// "i/o timeout", which is a dial error too, but the fix differs — a
	// firewall silently dropping packets rather than refusing them.
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline exceeded"):
		return PollErrorTimeout
	case strings.Contains(message, "connection refused"),
		strings.Contains(message, "no route to host"),
		strings.Contains(message, "no such host"),
		strings.Contains(message, "network is unreachable"),
		strings.Contains(message, "connection reset"):
		return PollErrorUnreachable
	case strings.Contains(message, "ssh session"), strings.Contains(message, "run probe"),
		strings.Contains(message, "exited with status"):
		return PollErrorCommand
	default:
		return PollErrorOther
	}
}

// pollErrorDetailMax bounds what is stored and shown. A wrapped SSH error can
// run long, and the column exists to explain a failure, not to hold a log.
const pollErrorDetailMax = 300

// TrimPollErrorDetail reduces an error to a single bounded line. Newlines are
// collapsed because the value is rendered inline in the UI.
func TrimPollErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	detail := strings.Join(strings.Fields(err.Error()), " ")
	if len(detail) > pollErrorDetailMax {
		// Back off until the cut lands on a rune boundary. Dropping trailing
		// continuation bytes is not enough: that leaves the lead byte of an
		// incomplete sequence, which is still invalid UTF-8 and which Postgres
		// rejects outright. At most three iterations.
		trimmed := detail[:pollErrorDetailMax]
		for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
			trimmed = trimmed[:len(trimmed)-1]
		}
		return strings.TrimSpace(trimmed) + "…"
	}
	return detail
}
