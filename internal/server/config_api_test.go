package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/config"
)

// serveConfig runs one request through the full handler chain (auth,
// recover, routing) — the same seam the phone-home tests use.
func serveConfig(t *testing.T, method, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	var r *bytes.Reader
	if body == nil {
		r = bytes.NewReader(nil)
	} else {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	rec := httptest.NewRecorder()
	srv.httpsrv.Handler.ServeHTTP(rec, req)
	return rec
}

// TestGetConfigRedactsLinear: GET /config returns the whole document with
// the Linear key reduced to api_key_set — never the value.
func TestGetConfigRedactsLinear(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	doc := config.Document{
		DefaultProvider: "anthropic",
		Providers:       map[string]config.Provider{"anthropic": {Backend: "acp", Command: "claude-agent-acp"}},
		Linear:          config.LinearConfig{APIKey: "lin_secret"},
		Repos:           map[string]config.RepoConfig{"a/b": {Path: "/p"}},
	}
	mustNil(t, doc.Save(home))

	rec := serveConfig(t, http.MethodGet, "/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "lin_secret") {
		t.Errorf("GET /config leaked the Linear key: %s", body)
	}
	if !strings.Contains(body, `"api_key_set":true`) {
		t.Errorf("want api_key_set:true, got: %s", body)
	}
	if !strings.Contains(body, "claude-agent-acp") {
		t.Errorf("non-secret fields dropped: %s", body)
	}
}

// TestPutConfigClearsLinearKey: PUT with an explicit clear signal drops the
// stored key (the Linear panel's Clear button), where an empty key alone
// would preserve it (config-screen-spec).
func TestPutConfigClearsLinearKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustNil(t, config.Document{Linear: config.LinearConfig{APIKey: "lin_secret"}}.Save(home))

	body, err := json.Marshal(config.Document{Linear: config.LinearConfig{ClearAPIKey: true}})
	mustNil(t, err)
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := config.LoadDocument(home)
	mustNil(t, err)
	if got.Linear.APIKey != "" {
		t.Errorf("clear left the key stored: %q", got.Linear.APIKey)
	}
}

// TestGetConfigMissingReturnsDefault: no config.json → the fresh default,
// with no key set.
func TestGetConfigMissingReturnsDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rec := serveConfig(t, http.MethodGet, "/config", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "anthropic") {
		t.Errorf("default document missing anthropic provider: %s", body)
	}
	if !strings.Contains(body, `"api_key_set":false`) {
		t.Errorf("fresh default should report api_key_set:false: %s", body)
	}
}

// TestPutConfigPersists: a valid document is stored and reloads equal.
func TestPutConfigPersists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	doc := config.Document{
		DefaultProvider: "anthropic",
		Providers:       map[string]config.Provider{"anthropic": {Backend: "acp", Command: "claude-agent-acp"}},
	}
	body, _ := json.Marshal(doc)
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := config.LoadDocument(home)
	mustNil(t, err)
	if got.Providers["anthropic"].Command != "claude-agent-acp" {
		t.Errorf("PUT did not persist providers: %#v", got.Providers)
	}
}

// TestPutConfigSecretMergeKeepsKey: an empty incoming api_key keeps the
// stored one.
func TestPutConfigSecretMergeKeepsKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustNil(t, config.Document{Linear: config.LinearConfig{APIKey: "kept"}}.Save(home))

	body, _ := json.Marshal(config.Document{Linear: config.LinearConfig{APIKey: ""}})
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := config.LoadDocument(home)
	mustNil(t, err)
	if got.Linear.APIKey != "kept" {
		t.Errorf("secret-merge dropped stored key, got %q", got.Linear.APIKey)
	}
}

// TestPutConfigSecretReplace: a non-empty incoming api_key replaces.
func TestPutConfigSecretReplace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustNil(t, config.Document{Linear: config.LinearConfig{APIKey: "old"}}.Save(home))

	body, _ := json.Marshal(config.Document{Linear: config.LinearConfig{APIKey: "new"}})
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := config.LoadDocument(home)
	mustNil(t, err)
	if got.Linear.APIKey != "new" {
		t.Errorf("replace failed, got %q", got.Linear.APIKey)
	}
}

// TestPutConfigRejectsUnknownBackend: a structural error fails the whole
// PUT (422) and stores nothing.
func TestPutConfigRejectsUnknownBackend(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustNil(t, config.Document{Providers: map[string]config.Provider{"anthropic": {Backend: "acp"}}}.Save(home))

	body, _ := json.Marshal(config.Document{Providers: map[string]config.Provider{"x": {Backend: "bogus"}}})
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	got, err := config.LoadDocument(home)
	mustNil(t, err)
	if _, ok := got.Providers["x"]; ok {
		t.Errorf("rejected PUT was persisted: %#v", got.Providers)
	}
}

// TestPutConfigRejectsDanglingDefault: default_provider naming no provider
// is a structural rejection.
func TestPutConfigRejectsDanglingDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	body, _ := json.Marshal(config.Document{
		DefaultProvider: "ghost",
		Providers:       map[string]config.Provider{"anthropic": {Backend: "acp"}},
	})
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

// TestPutConfigWarnsBadRepoPath: an unresolved repo path saves with a
// warning (soft), not a rejection.
func TestPutConfigWarnsBadRepoPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	body, _ := json.Marshal(config.Document{Repos: map[string]config.RepoConfig{"a/b": {Path: "/no/such/dir"}}})
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	mustNil(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	if len(resp.Warnings) != 1 {
		t.Errorf("want one warning, got %#v", resp.Warnings)
	}
	got, err := config.LoadDocument(home)
	mustNil(t, err)
	if _, ok := got.Repos["a/b"]; !ok {
		t.Errorf("warned PUT should still persist: %#v", got.Repos)
	}
}

// TestPutConfigRoundTripsRunnerImageAndRegistry: a document carrying a repo's
// declared runner + vm.image and the vm_images registry survives a PUT and
// resurfaces on GET — the Config Repos panel's save-then-reload path. Guards
// the whole-doc contract that a save never drops a repo's placement or the
// image registry (per-repo VM config, VM4).
func TestPutConfigRoundTripsRunnerImageAndRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	doc := config.Document{
		Repos: map[string]config.RepoConfig{
			"a/b": {Path: home, Runner: "vm", VM: &config.VMConfig{Image: "node-ts"}},
		},
		VMImages: map[string]string{"default": ".#vm-runner", "node-ts": ".#vm-runner"},
	}
	body, _ := json.Marshal(doc)
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// Persisted to disk.
	got, err := config.LoadDocument(home)
	mustNil(t, err)
	rc := got.Repos["a/b"]
	if rc.Runner != "vm" || rc.VM == nil || rc.VM.Image != "node-ts" {
		t.Errorf("PUT dropped the repo runner/image: %#v", rc)
	}
	if got.VMImages["node-ts"] == "" {
		t.Errorf("PUT dropped the vm_images registry: %#v", got.VMImages)
	}
	// And resurfaces on GET (so the panel can repaint the selects).
	rec = serveConfig(t, http.MethodGet, "/config", nil)
	getBody := rec.Body.String()
	for _, want := range []string{`"runner":"vm"`, `"image":"node-ts"`, `"vm_images"`} {
		if !strings.Contains(getBody, want) {
			t.Errorf("GET /config missing %q after round-trip: %s", want, getBody)
		}
	}
}

// TestPutConfigPreservesVMImagesWhenOmitted: the vm_images registry is
// server-owned (nix/CLI-managed, never edited via the UI). A PUT that omits it
// — any non-UI client, or the UI once it stops echoing — must keep the stored
// registry, not wipe it (per-repo VM config, VM4). Mirrors the secret merge:
// omitted ⇒ preserve.
func TestPutConfigPreservesVMImagesWhenOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustNil(t, config.Document{
		VMImages: map[string]string{"default": ".#vm-runner", "node-ts": ".#vm-runner"},
	}.Save(home))

	// A PUT carrying no vm_images at all (a bare curl/script client).
	body, _ := json.Marshal(config.Document{
		Repos: map[string]config.RepoConfig{"a/b": {Path: home}},
	})
	rec := serveConfig(t, http.MethodPut, "/config", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := config.LoadDocument(home)
	mustNil(t, err)
	if got.VMImages["node-ts"] == "" || got.VMImages["default"] == "" {
		t.Errorf("PUT omitting vm_images wiped the registry: %#v", got.VMImages)
	}
}

// TestPutConfigMalformedJSON: a body that isn't a document is a 400.
func TestPutConfigMalformedJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rec := serveConfig(t, http.MethodPut, "/config", []byte(`{ not json`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
