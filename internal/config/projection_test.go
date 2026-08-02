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

// TestRepoForPath: a registered checkout path reverse-resolves to its repo
// ref, bridging cwd-only dispatch (automations, raw dot) to the identity
// that keys per-repo checks (config-screen-spec C3).
func TestRepoForPath(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{
		"a/b": {Path: "/home/agent/Ghost"},
		"c/d": {Path: "/home/agent/Other"},
	}}
	if got, ok := doc.RepoForPath("/home/agent/Ghost"); !ok || got != "a/b" {
		t.Errorf("RepoForPath = %q, %v; want a/b, true", got, ok)
	}
	if got, ok := doc.RepoForPath("/unregistered"); ok || got != "" {
		t.Errorf("RepoForPath(miss) = %q, %v; want \"\", false", got, ok)
	}
	if _, ok := doc.RepoForPath(""); ok {
		t.Error("RepoForPath(empty) should not match a repo")
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

// TestRepoRunner: a repo's declared runner projects out for dispatch;
// an unregistered ref or an undeclared runner yields "" so the daemon
// default applies (per-repo VM config, VM2).
func TestRepoRunner(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{
		"a/b": {Path: "/p", Runner: "vm"},
		"c/d": {Path: "/q"},
	}}
	if got := doc.RepoRunner("a/b"); got != "vm" {
		t.Errorf("RepoRunner(a/b) = %q, want vm", got)
	}
	if got := doc.RepoRunner("c/d"); got != "" {
		t.Errorf("RepoRunner(undeclared) = %q, want \"\"", got)
	}
	if got := doc.RepoRunner("x/y"); got != "" {
		t.Errorf("RepoRunner(unregistered) = %q, want \"\"", got)
	}
}

// TestRepoImage: a repo's declared VM image projects out; an
// unregistered ref, an undeclared image, or a runner with no vm block
// yields "" so the "default" image applies (per-repo VM config, VM2).
func TestRepoImage(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{
		"a/b": {Path: "/p", Runner: "vm", VM: &VMConfig{Image: "node-ts"}},
		"c/d": {Path: "/q", Runner: "vm"},
	}}
	if got := doc.RepoImage("a/b"); got != "node-ts" {
		t.Errorf("RepoImage(a/b) = %q, want node-ts", got)
	}
	if got := doc.RepoImage("c/d"); got != "" {
		t.Errorf("RepoImage(no vm block) = %q, want \"\"", got)
	}
	if got := doc.RepoImage("x/y"); got != "" {
		t.Errorf("RepoImage(unregistered) = %q, want \"\"", got)
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
