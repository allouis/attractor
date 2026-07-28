package router

import (
	"strings"
	"testing"

	acpbackend "github.com/allouis/attractor/internal/backend/acp"
	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
)

func node(attrs map[string]string) *graph.Node {
	return &graph.Node{ID: "plan", Attrs: attrs}
}

func TestRouter_ResolvesACPBackendFromProvider(t *testing.T) {
	r := New(config.Config{Providers: map[string]config.Provider{
		"anthropic": {Backend: "acp", Command: "claude-agent-acp", ModelEnv: "ANTHROPIC_MODEL"},
	}})
	be, err := r.backendFor(node(map[string]string{"llm_provider": "anthropic"}))
	if err != nil {
		t.Fatal(err)
	}
	acp, ok := be.(*acpbackend.Backend)
	if !ok {
		t.Fatalf("expected *acp.Backend, got %T", be)
	}
	if acp.Command != "claude-agent-acp" || acp.ModelEnv != "ANTHROPIC_MODEL" {
		t.Fatalf("provider config not applied: %+v", acp)
	}
}

func TestRouter_CachesOneBackendPerProvider(t *testing.T) {
	r := New(config.Config{Providers: map[string]config.Provider{
		"anthropic": {Backend: "acp", Command: "claude-agent-acp"},
	}})
	n := node(map[string]string{"llm_provider": "anthropic"})
	first, err := r.backendFor(n)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.backendFor(n)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("expected the same cached backend instance per provider")
	}
}

func TestRouter_EmptyProviderSimulates(t *testing.T) {
	r := New(config.Config{}) // no default, no providers
	res, err := r.Run(engine.HandlerEnv{Node: node(map[string]string{})}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ResponseText, "[simulated] plan") {
		t.Fatalf("unresolved provider should simulate, got %q", res.ResponseText)
	}
}

func TestRouter_UnconfiguredProviderErrors(t *testing.T) {
	r := New(config.Config{DefaultProvider: "anthropic"}) // named but no table
	_, err := r.backendFor(node(map[string]string{}))
	if err == nil || !strings.Contains(err.Error(), "anthropic") {
		t.Fatalf("expected unconfigured-provider error, got %v", err)
	}
}

func TestRouter_ClaudecodeIsDeferred(t *testing.T) {
	r := New(config.Config{Providers: map[string]config.Provider{
		"anthropic-cli": {Backend: "claudecode"},
	}})
	_, err := r.backendFor(node(map[string]string{"llm_provider": "anthropic-cli"}))
	if err == nil || !strings.Contains(err.Error(), "claudecode") {
		t.Fatalf("expected claudecode-deferred error, got %v", err)
	}
}

func TestRouter_UnknownBackendErrors(t *testing.T) {
	r := New(config.Config{Providers: map[string]config.Provider{
		"weird": {Backend: "telepathy"},
	}})
	_, err := r.backendFor(node(map[string]string{"llm_provider": "weird"}))
	if err == nil || !strings.Contains(err.Error(), "telepathy") {
		t.Fatalf("expected unknown-backend error, got %v", err)
	}
}
