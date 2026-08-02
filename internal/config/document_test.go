package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestLoadDocumentMissingReturnsDefault: no config.json → the fresh
// default (no migration), which seeds a ready-to-edit anthropic provider
// entry but selects no default provider, so a bare run still simulates.
func TestLoadDocumentMissingReturnsDefault(t *testing.T) {
	doc, err := LoadDocument(t.TempDir())
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if _, ok := doc.Providers["anthropic"]; !ok {
		t.Errorf("default document missing anthropic provider: %#v", doc.Providers)
	}
	if doc.DefaultProvider != "" {
		t.Errorf("DefaultProvider = %q, want unset (bare runs simulate)", doc.DefaultProvider)
	}
}

// TestLoadDocumentParsesJSON: providers, the Linear key, and per-repo
// nested path+checks all round-trip through the JSON schema.
func TestLoadDocumentParsesJSON(t *testing.T) {
	home := t.TempDir()
	writeConfigJSON(t, home, `{
	  "default_provider": "anthropic",
	  "providers": {"anthropic": {"backend": "acp", "command": "claude-agent-acp", "model_env": "ANTHROPIC_MODEL"}},
	  "linear": {"api_key": "lin_abc"},
	  "repos": {"TryGhost/Ghost": {"path": "/home/agent/Ghost", "checks": {"deps": "pnpm install", "test": "pnpm test"}}}
	}`)

	doc, err := LoadDocument(home)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if doc.Providers["anthropic"].Command != "claude-agent-acp" {
		t.Errorf("provider command = %q", doc.Providers["anthropic"].Command)
	}
	if doc.Linear.APIKey != "lin_abc" {
		t.Errorf("linear api_key = %q", doc.Linear.APIKey)
	}
	repo, ok := doc.Repos["TryGhost/Ghost"]
	if !ok {
		t.Fatalf("repo missing: %#v", doc.Repos)
	}
	if repo.Path != "/home/agent/Ghost" || repo.Checks["deps"] != "pnpm install" || repo.Checks["test"] != "pnpm test" {
		t.Errorf("repo not parsed: %#v", repo)
	}
}

// TestSaveThenLoadRoundtrips: Save writes a document LoadDocument reads back equal.
func TestSaveThenLoadRoundtrips(t *testing.T) {
	home := t.TempDir()
	want := Document{
		DefaultProvider: "anthropic",
		Providers:       map[string]Provider{"anthropic": {Backend: "acp", Command: "claude-agent-acp", ModelEnv: "ANTHROPIC_MODEL"}},
		Linear:          LinearConfig{APIKey: "lin_xyz"},
		Repos:           map[string]RepoConfig{"a/b": {Path: "/p", Checks: map[string]string{"lint": "golint"}}},
	}
	if err := want.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadDocument(home)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %#v\nwant %#v", got, want)
	}
}

// TestSavePermissions0600: the file holds secrets, so it is owner-only.
func TestSavePermissions0600(t *testing.T) {
	home := t.TempDir()
	if err := DefaultDocument().Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, ".attractor", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.json perm = %o, want 600", perm)
	}
}

// TestLoadDocumentMalformedErrors: a corrupt file surfaces an error rather
// than silently falling back to the default (which would mask data loss).
func TestLoadDocumentMalformedErrors(t *testing.T) {
	home := t.TempDir()
	writeConfigJSON(t, home, `{ not json`)
	if _, err := LoadDocument(home); err == nil {
		t.Fatal("expected error on malformed config.json, got nil")
	}
}

// TestLoadDocumentParsesRepoRunnerAndImage: a repo may declare its dev
// environment — a named runner and, for a VM, a named image — and both
// round-trip through the JSON schema (per-repo VM config, VM2).
func TestLoadDocumentParsesRepoRunnerAndImage(t *testing.T) {
	home := t.TempDir()
	writeConfigJSON(t, home, `{
	  "repos": {"TryGhost/Ghost": {"path": "/home/agent/Ghost", "runner": "vm", "vm": {"image": "node-ts"}}}
	}`)

	doc, err := LoadDocument(home)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	repo := doc.Repos["TryGhost/Ghost"]
	if repo.Runner != "vm" {
		t.Errorf("Runner = %q, want vm", repo.Runner)
	}
	if repo.VM == nil || repo.VM.Image != "node-ts" {
		t.Errorf("VM = %#v, want image node-ts", repo.VM)
	}
}

// TestSaveOmitsUnsetRunnerAndVM: a repo declaring neither runner nor image
// serializes without those keys, so existing config files stay byte-
// unchanged (matches the vm_images omitempty precedent, VM2).
func TestSaveOmitsUnsetRunnerAndVM(t *testing.T) {
	home := t.TempDir()
	doc := Document{Repos: map[string]RepoConfig{"a/b": {Path: "/p", Checks: map[string]string{"lint": "golint"}}}}
	if err := doc.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".attractor", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); strings.Contains(s, "\"runner\"") || strings.Contains(s, "\"vm\"") {
		t.Errorf("unset runner/vm leaked into serialized config:\n%s", s)
	}
}

// TestSaveThenLoadRoundtripsRunnerImage: a declared runner + vm image
// survive Save then LoadDocument unchanged.
func TestSaveThenLoadRoundtripsRunnerImage(t *testing.T) {
	home := t.TempDir()
	want := Document{Repos: map[string]RepoConfig{
		"a/b": {Path: "/p", Runner: "vm", VM: &VMConfig{Image: "node-ts"}},
	}}
	if err := want.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := LoadDocument(home)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func writeConfigJSON(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, ".attractor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestLoadDocumentVMImages: a config.json with a vm_images block round-trips
// through LoadDocument into the Document.VMImages registry (VM1).
func TestLoadDocumentVMImages(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".attractor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"vm_images":{"default":".#vm-runner","python":"/nix/store/x-run-nixos-vm-python"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := LoadDocument(home)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	want := map[string]string{"default": ".#vm-runner", "python": "/nix/store/x-run-nixos-vm-python"}
	if !reflect.DeepEqual(doc.VMImages, want) {
		t.Errorf("VMImages = %v, want %v", doc.VMImages, want)
	}
}
