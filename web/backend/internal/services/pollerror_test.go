package services

import (
	"errors"
	"strings"
	"testing"
)

func TestClassifyPollErrorAuthenticationVariants(t *testing.T) {
	for _, message := range []string{
		"ssh handshake: ssh: unable to authenticate, attempted methods [none publickey]",
		"ssh: no authentication methods configured",
		"authentication failed",
	} {
		if got := ClassifyPollError(errors.New(message)); got != PollErrorAuth {
			t.Errorf("ClassifyPollError(%q) = %q, want %q", message, got, PollErrorAuth)
		}
	}
}

func TestTrimPollErrorDetailNormalizesAndBoundsUTF8(t *testing.T) {
	detail := "  connection\n refused\tby peer  "
	if got := TrimPollErrorDetail(errors.New(detail)); got != "connection refused by peer" {
		t.Fatalf("TrimPollErrorDetail() = %q, want normalized detail", got)
	}

	long := strings.Repeat("界", pollErrorDetailMax)
	got := TrimPollErrorDetail(errors.New(long))
	if len([]rune(got)) > pollErrorDetailMax+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("TrimPollErrorDetail() returned %d runes without a valid bound: %q", len([]rune(got)), got)
	}
}
