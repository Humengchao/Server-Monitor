package services

import (
	"strings"
	"unicode/utf8"
)

// PollErrorKind is intentionally small and stable: the UI translates these
// values into advice while the bounded detail preserves the useful raw error.
type PollErrorKind string

const (
	PollErrorAuth        PollErrorKind = "auth"
	PollErrorHostKey     PollErrorKind = "host_key"
	PollErrorUnreachable PollErrorKind = "unreachable"
	PollErrorTimeout     PollErrorKind = "timeout"
	PollErrorCommand     PollErrorKind = "command"
	PollErrorStorage     PollErrorKind = "storage"
	PollErrorOther       PollErrorKind = "other"
)

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
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline exceeded"):
		return PollErrorTimeout
	case strings.Contains(message, "connection refused"), strings.Contains(message, "no route to host"), strings.Contains(message, "no such host"), strings.Contains(message, "network is unreachable"), strings.Contains(message, "connection reset"):
		return PollErrorUnreachable
	case strings.Contains(message, "ssh session"), strings.Contains(message, "run probe"), strings.Contains(message, "exited with status"):
		return PollErrorCommand
	default:
		return PollErrorOther
	}
}

const pollErrorDetailMax = 300

func TrimPollErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	detail := strings.Join(strings.Fields(err.Error()), " ")
	if len(detail) <= pollErrorDetailMax {
		return detail
	}
	trimmed := detail[:pollErrorDetailMax]
	for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return strings.TrimSpace(trimmed) + "…"
}
