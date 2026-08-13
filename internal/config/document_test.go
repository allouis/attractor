package config

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestLoadDocumentParsesJSON: the provider registry round-trips through
// the JSON schema; unknown keys from richer historical configs (linear,
// repos, vm_images) are ignored rather than erroring.
func TestLoadDocumentParsesJSON(t *testing.T) {
	home := t.TempDir()
	writeConfigJSON(t, home, `{
	  "default_provider": "anthropic",
	  "providers": {"anthropic": {"backend": "acp", "command": "claude-agent-acp", "model_env": "ANTHROPIC_MODEL"}},
	  "linear": {"api_key": "lin_abc"},
	  "repos": {"TryGhost/Ghost": {"path": "/home/agent/Ghost"}}
	}`)

	doc, err := LoadDocument(home)
	if err != nil {
		t.Fatalf("LoadDocument: %v", err)
	}
	if doc.Providers["anthropic"].Command != "claude-agent-acp" {
		t.Errorf("provider command = %q", doc.Providers["anthropic"].Command)
	}
	if doc.DefaultProvider != "anthropic" {
		t.Errorf("default_provider = %q", doc.DefaultProvider)
	}
}

// TestSaveThenLoadRoundtrips: Save writes a document LoadDocument reads back equal.
func TestSaveThenLoadRoundtrips(t *testing.T) {
	home := t.TempDir()
	want := Document{
		DefaultProvider: "anthropic",
		Providers:       map[string]Provider{"anthropic": {Backend: "acp", Command: "claude-agent-acp", ModelEnv: "ANTHROPIC_MODEL"}},
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

// TestSavePermissions0600: owner-only, in case a provider entry ever
// carries something sensitive.
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
