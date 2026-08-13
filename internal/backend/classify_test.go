package backend

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifyTurn_NilStaysNil(t *testing.T) {
	if got := ClassifyTurn(nil); got != nil {
		t.Fatalf("ClassifyTurn(nil) = %v, want nil", got)
	}
}

// ClassifyTurn is called on errors from an in-flight agent turn, where the
// default is transient (the session was already up; whatever broke it is
// transport-shaped). Only fatal-shaped messages (auth/config/validation)
// escape the wrap.
func TestClassifyTurn(t *testing.T) {
	cases := []struct {
		msg       string
		transient bool
	}{
		// Fatal: retrying cannot help.
		{"401 unauthorized", false},
		{"authentication_error: invalid x-api-key", false},
		{"invalid API key provided", false},
		{"403 Forbidden", false},
		{"400 invalid_request_error: max_tokens too large", false},
		{"billing hard limit reached", false},
		{"credit balance is too low", false},
		// Transient: network / provider blips.
		{"connection reset by peer", true},
		{"429 Too Many Requests", true},
		{"500 Internal Server Error", true},
		{"overloaded_error", true},
		{"context deadline exceeded", true},
		{"unexpected EOF", true},
		// Unknown mid-turn errors default to transient.
		{"agent process exited unexpectedly", true},
	}
	for _, c := range cases {
		err := ClassifyTurn(errors.New(c.msg))
		if got := IsTransient(err); got != c.transient {
			t.Errorf("ClassifyTurn(%q): IsTransient = %v, want %v", c.msg, got, c.transient)
		}
	}
}

func TestTransient_MarkSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("acp: %w", Transient(errors.New("connection reset")))
	if !IsTransient(err) {
		t.Fatal("Transient mark lost through fmt.Errorf %w wrapping")
	}
}

func TestIsTransient_PlainErrorIsNot(t *testing.T) {
	if IsTransient(errors.New("boom")) {
		t.Fatal("plain error must not be transient")
	}
	if IsTransient(nil) {
		t.Fatal("nil must not be transient")
	}
}

func TestTransientMessage(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"429 rate limit exceeded", true},
		{"overloaded_error: Overloaded", true},
		{"529 overloaded", true},
		{"connection refused", true},
		{"stream error: unexpected EOF", true},
		{"invalid api key", false},
		{"task failed: tests do not pass", false},
	}
	for _, c := range cases {
		if got := TransientMessage(c.msg); got != c.want {
			t.Errorf("TransientMessage(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}
