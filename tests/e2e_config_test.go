package attractor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fabro/attractor/internal/config"
	"github.com/fabro/attractor/internal/graph"
)

func TestConfig_ParseProviders(t *testing.T) {
	src := `
# machine-local config
default_provider = "anthropic"

[providers.anthropic]
backend   = "acp"
command   = "claude-agent-acp"
model_env = "ANTHROPIC_MODEL"   # trailing comment

[providers.openai]
backend = "acp"
command = "codex-acp"
model_env = "CODEX_MODEL"
`
	cfg, err := config.Parse([]byte(src))
	must(t, err)
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

func TestConfig_LoadOverlaysCwdOverHome(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeConfig(t, filepath.Join(home, ".attractor", "config.toml"), `
default_provider = "anthropic"
[providers.anthropic]
backend = "acp"
command = "home-agent"
`)
	writeConfig(t, filepath.Join(cwd, ".attractor", "config.toml"), `
[providers.anthropic]
command = "cwd-agent"
`)
	cfg, err := config.Load(home, cwd)
	must(t, err)
	// default_provider from home survives (cwd file doesn't set it).
	if cfg.DefaultProvider != "anthropic" {
		t.Fatalf("default_provider=%q, want anthropic", cfg.DefaultProvider)
	}
	// cwd command overrides home command.
	if got := cfg.Providers["anthropic"].Command; got != "cwd-agent" {
		t.Fatalf("command=%q, want cwd-agent (cwd overlay)", got)
	}
}

func TestConfig_LoadMissingFilesIsEmpty(t *testing.T) {
	cfg, err := config.Load(t.TempDir(), t.TempDir())
	must(t, err)
	if cfg.DefaultProvider != "" || len(cfg.Providers) != 0 {
		t.Fatalf("expected empty config, got %+v", cfg)
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

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Dir(path), 0o755))
	must(t, os.WriteFile(path, []byte(body), 0o644))
}
