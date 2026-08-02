package attractor_test

import (
	"testing"

	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/graph"
)

func TestConfig_LoadProviders(t *testing.T) {
	home := t.TempDir()
	saveConfig(t, home, config.Document{
		DefaultProvider: "anthropic",
		Providers: map[string]config.Provider{
			"anthropic": {Backend: "acp", Command: "claude-agent-acp", ModelEnv: "ANTHROPIC_MODEL"},
			"openai":    {Backend: "acp", Command: "codex-acp", ModelEnv: "CODEX_MODEL"},
		},
	})
	doc, err := config.LoadDocument(home)
	must(t, err)
	cfg := doc.ProviderConfig()
	if cfg.DefaultProvider != "anthropic" {
		t.Fatalf("default_provider=%q, want anthropic", cfg.DefaultProvider)
	}
	a, ok := cfg.Providers["anthropic"]
	if !ok {
		t.Fatalf("missing anthropic provider: %+v", cfg.Providers)
	}
	if a.Backend != "acp" || a.Command != "claude-agent-acp" || a.ModelEnv != "ANTHROPIC_MODEL" {
		t.Fatalf("anthropic provider wrong: %+v", a)
	}
	o := cfg.Providers["openai"]
	if o.Command != "codex-acp" || o.ModelEnv != "CODEX_MODEL" {
		t.Fatalf("openai provider wrong: %+v", o)
	}
}

func TestConfig_LoadMissingReturnsDefault(t *testing.T) {
	doc, err := config.LoadDocument(t.TempDir())
	must(t, err)
	// A missing config.json yields the fresh default (no migration): a
	// ready-to-edit anthropic provider entry, but no default_provider so a
	// bare run still simulates.
	if _, ok := doc.Providers["anthropic"]; !ok {
		t.Fatalf("fresh default missing anthropic provider: %+v", doc.Providers)
	}
	if doc.DefaultProvider != "" {
		t.Fatalf("default_provider=%q, want unset (bare runs simulate)", doc.DefaultProvider)
	}
}

func TestConfig_InferProvider(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":  "anthropic",
		"claude-haiku-4-5": "anthropic",
		"gpt-5":            "openai",
		"o3":               "openai",
		"gemini-2.5-pro":   "google",
		"llama-3":          "",
		"":                 "",
	}
	for model, want := range cases {
		if got := config.InferProvider(model); got != want {
			t.Fatalf("InferProvider(%q)=%q, want %q", model, got, want)
		}
	}
}

func TestConfig_ResolveProviderPrecedence(t *testing.T) {
	cfg := config.Config{DefaultProvider: "anthropic"}

	// explicit llm_provider wins over everything
	n := &graph.Node{Attrs: map[string]string{"llm_provider": "openai", "llm_model": "claude-opus-4-8"}}
	if got := cfg.ResolveProvider(n); got != "openai" {
		t.Fatalf("explicit provider: got %q, want openai", got)
	}
	// inferred from model prefix when no explicit provider
	n = &graph.Node{Attrs: map[string]string{"llm_model": "gpt-5"}}
	if got := cfg.ResolveProvider(n); got != "openai" {
		t.Fatalf("inferred provider: got %q, want openai", got)
	}
	// default when neither present
	n = &graph.Node{Attrs: map[string]string{}}
	if got := cfg.ResolveProvider(n); got != "anthropic" {
		t.Fatalf("default provider: got %q, want anthropic", got)
	}
}

// saveConfig writes the daemon-owned config.json under home, the storage
// both the CLI and daemon read (config-screen-spec).
func saveConfig(t *testing.T, home string, doc config.Document) {
	t.Helper()
	must(t, doc.Save(home))
}
