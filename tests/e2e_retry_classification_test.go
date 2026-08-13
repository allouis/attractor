package attractor_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// These tests pin D1 of the local-first plan (spec §3.5–3.6): backend
// errors are classified at the boundary. Transient failures (network,
// 429/5xx, stalls) surface as RETRY outcomes and the engine's retry
// machinery re-runs the node with backoff; fatal failures (auth, config,
// validation) fail the node immediately, no retry.

const retrySrc = `digraph t {
	default_max_retries=2
	start [shape=Mdiamond]
	work [prompt="x"]
	done [shape=Msquare]
	start -> work -> done
}`

func TestRetry_TransientErrorRetriesThenSucceeds(t *testing.T) {
	be := fake.New()
	be.SetSequence("work",
		fake.Step{Err: backend.Transient(errors.New("connection reset by peer"))},
		fake.Step{Err: backend.Transient(errors.New("429 Too Many Requests"))},
		fake.Step{Text: "ok on third attempt"},
	)
	out, events, _ := runFixture(t, retrySrc, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status = %s (reason %q), want success after retries", out.Status, out.FailureReason)
	}
	if got := be.CallCount("work"); got != 3 {
		t.Fatalf("backend called %d times, want 3 (initial + 2 retries)", got)
	}
	retrying := eventsOfKind(events, engine.EventStageRetrying)
	if len(retrying) != 2 {
		t.Fatalf("saw %d stage_retrying events, want 2", len(retrying))
	}
	// The retry event must carry the classified failure reason so the
	// event log explains *why* the node retried.
	if !strings.Contains(retrying[0].Message, "connection reset") {
		t.Errorf("first retry event message = %q, want the transient error text", retrying[0].Message)
	}
}

func TestRetry_FatalErrorFailsImmediately(t *testing.T) {
	be := fake.New()
	be.SetSequence("work",
		fake.Step{Err: errors.New("authentication_error: invalid x-api-key")},
		fake.Step{Text: "must never be reached"},
	)
	out, events, _ := runFixture(t, retrySrc, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("status = %s, want fail", out.Status)
	}
	if got := be.CallCount("work"); got != 1 {
		t.Fatalf("backend called %d times, want 1 (no retry on fatal error)", got)
	}
	if n := len(eventsOfKind(events, engine.EventStageRetrying)); n != 0 {
		t.Fatalf("saw %d stage_retrying events, want 0", n)
	}
}

func TestRetry_ExhaustedRetriesFailTheNode(t *testing.T) {
	be := fake.New()
	be.SetSequence("work",
		fake.Step{Err: backend.Transient(errors.New("500 Internal Server Error"))},
	)
	out, _, _ := runFixture(t, retrySrc, be, nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("status = %s, want fail after exhausting retries", out.Status)
	}
	if got := be.CallCount("work"); got != 3 {
		t.Fatalf("backend called %d times, want 3 (initial + 2 retries)", got)
	}
}

func eventsOfKind(events []engine.Event, kind engine.EventKind) []engine.Event {
	var out []engine.Event
	for _, ev := range events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}
