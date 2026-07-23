package attractor_test

import (
	"testing"

	"github.com/fabro/attractor/internal/config"
	"github.com/fabro/attractor/internal/lint"
)

func providerRules(cfg config.Config) []lint.Rule {
	return []lint.Rule{
		lint.ProviderKnownRule{Config: cfg},
		lint.ModelEnvMissingRule{Config: cfg},
	}
}

func TestLint_ProviderKnownWarnsForUnconfigured(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.Provider{
		"anthropic": {Backend: "acp"},
	}}
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		plan  [prompt="p", llm_provider="ghost"]
		done  [shape=Msquare]
		start -> plan -> done
	}`)
	diags := lint.Validate(g, providerRules(cfg)...)
	if !hasDiag(diags, "provider_known", lint.Warning) {
		t.Fatalf("expected provider_known warning for unconfigured provider: %+v", diags)
	}
}

func TestLint_ProviderKnownQuietForConfigured(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.Provider{
		"anthropic": {Backend: "acp"},
	}}
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		plan  [prompt="p", llm_provider="anthropic"]
		done  [shape=Msquare]
		start -> plan -> done
	}`)
	diags := lint.Validate(g, providerRules(cfg)...)
	if hasDiag(diags, "provider_known", lint.Warning) {
		t.Fatalf("configured provider should not warn: %+v", diags)
	}
}

func TestLint_ModelEnvMissingWarns(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.Provider{
		"anthropic": {Backend: "acp"}, // no model_env
	}}
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		plan  [prompt="p", llm_provider="anthropic", llm_model="claude-opus-4-8"]
		done  [shape=Msquare]
		start -> plan -> done
	}`)
	diags := lint.Validate(g, providerRules(cfg)...)
	if !hasDiag(diags, "model_env_missing", lint.Warning) {
		t.Fatalf("expected model_env_missing warning: %+v", diags)
	}
}

func TestLint_ModelEnvPresentQuiet(t *testing.T) {
	cfg := config.Config{Providers: map[string]config.Provider{
		"anthropic": {Backend: "acp", ModelEnv: "ANTHROPIC_MODEL"},
	}}
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		plan  [prompt="p", llm_provider="anthropic", llm_model="claude-opus-4-8"]
		done  [shape=Msquare]
		start -> plan -> done
	}`)
	diags := lint.Validate(g, providerRules(cfg)...)
	if hasDiag(diags, "model_env_missing", lint.Warning) {
		t.Fatalf("provider with model_env should not warn: %+v", diags)
	}
}
