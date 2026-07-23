package attractor_test

import (
	"strings"
	"testing"

	acpbackend "github.com/fabro/attractor/internal/backend/acp"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/graph"
)

// TestACPBackend_SessionReuseUnderFullFidelity: two stages sharing a
// thread under full fidelity resume the same agent session via
// session/load rather than starting fresh.
func TestACPBackend_SessionReuseUnderFullFidelity(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t)}

	env1, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	env1.Fidelity = engine.FidelityFull
	env1.ThreadID = "thread-1"
	r1, err := b.Run(env1, "first stage")
	must(t, err)
	if strings.Contains(r1.ResponseText, "resumed=") {
		t.Fatalf("first stage must start a fresh session, got %q", r1.ResponseText)
	}

	env2, _ := acpEnv(t, &graph.Node{ID: "review", Attrs: map[string]string{}})
	env2.Fidelity = engine.FidelityFull
	env2.ThreadID = "thread-1"
	r2, err := b.Run(env2, "second stage")
	must(t, err)
	if !strings.Contains(r2.ResponseText, "resumed=fake-sess-1") {
		t.Fatalf("second stage should load the recorded session, got %q", r2.ResponseText)
	}
}

// TestACPBackend_NoReuseWithoutFullFidelity: non-full fidelity always
// starts a fresh session even when the thread has one recorded.
func TestACPBackend_NoReuseWithoutFullFidelity(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t)}

	env1, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	env1.Fidelity = engine.FidelityFull
	env1.ThreadID = "thread-1"
	_, err := b.Run(env1, "first stage")
	must(t, err)

	env2, _ := acpEnv(t, &graph.Node{ID: "review", Attrs: map[string]string{}})
	env2.Fidelity = engine.FidelityCompact
	env2.ThreadID = "thread-1"
	r2, err := b.Run(env2, "second stage")
	must(t, err)
	if strings.Contains(r2.ResponseText, "resumed=") {
		t.Fatalf("compact fidelity must not resume, got %q", r2.ResponseText)
	}
}

// TestACPBackend_NoReuseWhenAgentLacksLoadSession: an agent that does
// not advertise loadSession gets a fresh session per stage.
func TestACPBackend_NoReuseWhenAgentLacksLoadSession(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t, "FAKE_ACP_MODE=noload")}

	env1, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	env1.Fidelity = engine.FidelityFull
	env1.ThreadID = "thread-1"
	_, err := b.Run(env1, "first stage")
	must(t, err)

	env2, _ := acpEnv(t, &graph.Node{ID: "review", Attrs: map[string]string{}})
	env2.Fidelity = engine.FidelityFull
	env2.ThreadID = "thread-1"
	r2, err := b.Run(env2, "second stage")
	must(t, err)
	if strings.Contains(r2.ResponseText, "resumed=") {
		t.Fatalf("agent without loadSession must not be asked to resume, got %q", r2.ResponseText)
	}
}
