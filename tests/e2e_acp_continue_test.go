package attractor_test

import (
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	acpbackend "github.com/allouis/attractor/internal/backend/acp"
	"github.com/allouis/attractor/internal/graph"
)

// The ACP backend supports in-session contract corrections (D2): it
// must satisfy backend.Continuer.
var _ backend.Continuer = (*acpbackend.Backend)(nil)

// TestACPBackend_ContinueResumesSameSession: a Continue after Run loads
// the exact session the Run used, regardless of fidelity/thread (a
// correction turn always targets the node's own just-finished session).
func TestACPBackend_ContinueResumesSameSession(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t)}
	env, _ := acpEnv(t, &graph.Node{ID: "synth", Attrs: map[string]string{}})

	r1, err := b.Run(env, "first turn")
	must(t, err)
	if strings.Contains(r1.ResponseText, "resumed=") {
		t.Fatalf("first turn must start fresh, got %q", r1.ResponseText)
	}

	r2, err := b.Continue(env, "write your status.json now")
	must(t, err)
	if !strings.Contains(r2.ResponseText, "resumed=fake-sess-1") {
		t.Fatalf("Continue must resume the Run's session, got %q", r2.ResponseText)
	}
}

// TestACPBackend_ContinueWithoutPriorRunErrors: a Continue with no
// recorded session for the node is a programming error and must not
// silently start a fresh session.
func TestACPBackend_ContinueWithoutPriorRunErrors(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t)}
	env, _ := acpEnv(t, &graph.Node{ID: "synth", Attrs: map[string]string{}})
	if _, err := b.Continue(env, "correction"); err == nil {
		t.Fatal("Continue without a prior Run must error")
	}
}

// TestACPBackend_ContinueNeedsLoadSession: an agent that does not
// advertise loadSession cannot host a correction turn; Continue must
// error rather than silently send the correction into a fresh session
// with no context.
func TestACPBackend_ContinueNeedsLoadSession(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t, "FAKE_ACP_MODE=noload")}
	env, _ := acpEnv(t, &graph.Node{ID: "synth", Attrs: map[string]string{}})
	_, err := b.Run(env, "first turn")
	must(t, err)
	if _, err := b.Continue(env, "correction"); err == nil {
		t.Fatal("Continue must error when the agent lacks loadSession")
	}
}
