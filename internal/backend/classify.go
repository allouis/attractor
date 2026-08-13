package backend

import (
	"errors"
	"strings"
)

// Error classification (spec §3.6, local-first plan D1). Backends decide
// at their boundary whether a failure is worth retrying. A transient
// error (network blip, 429/5xx, stall) is wrapped with Transient so the
// codergen handler maps it to a RETRY outcome and the engine's retry
// machinery re-runs the node with backoff. Everything else (auth,
// config, validation) stays a plain error and fails the node
// immediately — retrying cannot fix a bad API key.

// transientError marks an error as retry-worthy. It participates in
// errors.As so the mark survives fmt.Errorf("%w") wrapping.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// Transient marks err as retryable. nil in, nil out.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return &transientError{err: err}
}

// IsTransient reports whether err carries the Transient mark anywhere in
// its chain.
func IsTransient(err error) bool {
	var t *transientError
	return errors.As(err, &t)
}

// ClassifyTurn classifies an error from an in-flight agent turn. Once a
// session is up, whatever broke it is transport-shaped, so the default
// is transient; only fatal-shaped messages (auth/config/validation)
// escape the wrap. Errors from before the turn (bad command line,
// missing binary) should not pass through here — they are config
// mistakes and callers return them unwrapped.
func ClassifyTurn(err error) error {
	if err == nil {
		return nil
	}
	if fatalMessage(err.Error()) {
		return err
	}
	return Transient(err)
}

// fatalPatterns match provider failures that no retry can fix. Matched
// case-insensitively as substrings.
var fatalPatterns = []string{
	"401", "403", "unauthorized", "forbidden",
	"authentication", "api key", "x-api-key",
	"invalid_request", "400 ",
	"billing", "credit balance",
	"permission_error",
}

// transientPatterns match provider failures that are worth retrying.
var transientPatterns = []string{
	"429", "rate limit", "too many requests",
	"500", "502", "503", "504", "529",
	"overloaded",
	"internal server error", "bad gateway", "service unavailable",
	"connection reset", "connection refused", "broken pipe",
	"unexpected eof", "eof",
	"timeout", "deadline exceeded", "stalled",
	"temporarily", "try again",
}

func fatalMessage(msg string) bool {
	m := strings.ToLower(msg)
	for _, p := range fatalPatterns {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}

// TransientMessage reports whether a failure message (e.g. an agent's
// self-reported error envelope) looks transient-shaped. Unlike
// ClassifyTurn there is no default-transient here: the message must
// positively match a transient pattern and not a fatal one, because an
// agent's error text usually describes a task failure, not a transport
// one.
func TransientMessage(msg string) bool {
	if fatalMessage(msg) {
		return false
	}
	m := strings.ToLower(msg)
	for _, p := range transientPatterns {
		if strings.Contains(m, p) {
			return true
		}
	}
	return false
}
