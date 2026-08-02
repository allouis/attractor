package config

import (
	"reflect"
	"testing"
)

// TestProviderConfigProjection: a Document projects to the legacy
// config.Config the router and Linear source consume.
func TestProviderConfigProjection(t *testing.T) {
	doc := Document{
		DefaultProvider: "anthropic",
		Providers:       map[string]Provider{"anthropic": {Backend: "acp", Command: "claude-agent-acp"}},
		Linear:          LinearConfig{APIKey: "lin_abc"},
	}
	cfg := doc.ProviderConfig()
	if cfg.DefaultProvider != "anthropic" {
		t.Errorf("DefaultProvider = %q", cfg.DefaultProvider)
	}
	if cfg.Providers["anthropic"].Command != "claude-agent-acp" {
		t.Errorf("provider not projected: %#v", cfg.Providers)
	}
	if cfg.LinearAPIKey != "lin_abc" {
		t.Errorf("LinearAPIKey = %q", cfg.LinearAPIKey)
	}
}

// TestProviderConfigEnvFallback: LINEAR_API_KEY fills the key when the
// document sets none, and the document wins when both are present
// (mirrors the retired config.toml env fallback, items-spec I2).
func TestProviderConfigEnvFallback(t *testing.T) {
	t.Setenv("LINEAR_API_KEY", "lin_env")

	cfg := Document{}.ProviderConfig()
	if cfg.LinearAPIKey != "lin_env" {
		t.Errorf("env fallback not applied: %q", cfg.LinearAPIKey)
	}

	cfg = Document{Linear: LinearConfig{APIKey: "lin_doc"}}.ProviderConfig()
	if cfg.LinearAPIKey != "lin_doc" {
		t.Errorf("document should win over env: %q", cfg.LinearAPIKey)
	}
}

// TestReposMapProjection: repos project to an owner/name → path map.
func TestReposMapProjection(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{
		"a/b": {Path: "/p"},
		"c/d": {Path: "/q"},
	}}
	got := doc.ReposMap()
	want := map[string]string{"a/b": "/p", "c/d": "/q"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReposMap = %#v, want %#v", got, want)
	}
}

// TestChecksForRepoMatch: the checks of the repo named by the run's ref
// (C3 keys by repo identity, not cwd).
func TestChecksForRepoMatch(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{
		"a/b": {Path: "/home/agent/Ghost", Checks: map[string]string{"deps": "pnpm install"}},
	}}
	got := doc.ChecksForRepo("a/b")
	if got["deps"] != "pnpm install" {
		t.Errorf("ChecksForRepo = %#v", got)
	}
}

// TestChecksForRepoMiss: an unregistered or empty repo ref yields nil.
func TestChecksForRepoMiss(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{"a/b": {Path: "/p"}}}
	if got := doc.ChecksForRepo("x/y"); got != nil {
		t.Errorf("ChecksForRepo(miss) = %#v, want nil", got)
	}
	if got := doc.ChecksForRepo(""); got != nil {
		t.Errorf("ChecksForRepo(empty) = %#v, want nil", got)
	}
}

// TestChecksForPathMatch: the checks of the repo whose path matches the
// run's cwd (C1 keys by cwd via reverse match; C3 re-keys by repo ref).
func TestChecksForPathMatch(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{
		"a/b": {Path: "/home/agent/Ghost", Checks: map[string]string{"deps": "pnpm install"}},
	}}
	got := doc.ChecksForPath("/home/agent/Ghost")
	if got["deps"] != "pnpm install" {
		t.Errorf("ChecksForPath = %#v", got)
	}
}

// TestChecksForPathMiss: an unregistered cwd yields nil.
func TestChecksForPathMiss(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{"a/b": {Path: "/p"}}}
	if got := doc.ChecksForPath("/other"); got != nil {
		t.Errorf("ChecksForPath(miss) = %#v, want nil", got)
	}
}
