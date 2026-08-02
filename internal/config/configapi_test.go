package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestRedactedHidesLinearKey: Redacted never echoes the Linear key — it
// reports only whether one is set (config-screen-spec: redact on read).
func TestRedactedHidesLinearKey(t *testing.T) {
	doc := Document{
		DefaultProvider: "anthropic",
		Providers:       map[string]Provider{"anthropic": {Backend: "acp", Command: "claude-agent-acp", ModelEnv: "ANTHROPIC_MODEL"}},
		Linear:          LinearConfig{APIKey: "lin_secret"},
		Repos:           map[string]RepoConfig{"a/b": {Path: "/p", Checks: map[string]string{"lint": "golint"}}},
	}
	data, err := json.Marshal(doc.Redacted())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "lin_secret") {
		t.Errorf("redacted output leaks the Linear key: %s", s)
	}
	if strings.Contains(s, `"api_key"`) {
		t.Errorf("redacted output carries an api_key field: %s", s)
	}
	if !strings.Contains(s, `"api_key_set":true`) {
		t.Errorf("redacted output should report api_key_set:true, got: %s", s)
	}
	// Non-secret fields survive redaction.
	if !strings.Contains(s, `"claude-agent-acp"`) || !strings.Contains(s, `"/p"`) {
		t.Errorf("redaction dropped non-secret fields: %s", s)
	}
}

// TestRedactedUnsetKey: an empty key reports api_key_set:false.
func TestRedactedUnsetKey(t *testing.T) {
	data, err := json.Marshal(Document{}.Redacted())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"api_key_set":false`) {
		t.Errorf("empty key should report api_key_set:false, got: %s", data)
	}
}

// TestValidateRejectsUnknownBackend: a provider backend outside
// acp|claudecode|simulation fails the whole PUT (nothing saved).
func TestValidateRejectsUnknownBackend(t *testing.T) {
	doc := Document{Providers: map[string]Provider{"x": {Backend: "bogus"}}}
	if _, err := doc.Validate(); err == nil {
		t.Fatal("expected error for unknown backend, got nil")
	}
}

// TestValidateRejectsEmptyBackend: an empty backend is not a valid
// mechanism, so it too is a structural rejection.
func TestValidateRejectsEmptyBackend(t *testing.T) {
	doc := Document{Providers: map[string]Provider{"x": {Backend: ""}}}
	if _, err := doc.Validate(); err == nil {
		t.Fatal("expected error for empty backend, got nil")
	}
}

// TestValidateRejectsDanglingDefaultProvider: default_provider must name a
// configured provider.
func TestValidateRejectsDanglingDefaultProvider(t *testing.T) {
	doc := Document{
		DefaultProvider: "ghost",
		Providers:       map[string]Provider{"anthropic": {Backend: "acp"}},
	}
	_, err := doc.Validate()
	if err == nil {
		t.Fatal("expected error for dangling default_provider, got nil")
	}
	if !strings.Contains(err.Error(), "default_provider") {
		t.Errorf("error should name default_provider, got: %v", err)
	}
}

// TestValidateEmptyDefaultProviderOK: an unset default_provider is valid —
// a fresh install simulates bare runs rather than selecting a provider.
func TestValidateEmptyDefaultProviderOK(t *testing.T) {
	doc := Document{Providers: map[string]Provider{"anthropic": {Backend: "acp"}}}
	if _, err := doc.Validate(); err != nil {
		t.Fatalf("empty default_provider should validate: %v", err)
	}
}

// TestValidateWarnsUnresolvedRepoPath: a repo path that doesn't resolve is
// a soft warning — the document still saves.
func TestValidateWarnsUnresolvedRepoPath(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{"a/b": {Path: "/no/such/dir"}}}
	warnings, err := doc.Validate()
	if err != nil {
		t.Fatalf("unresolved path should warn, not reject: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "a/b") {
		t.Errorf("want one warning naming a/b, got: %#v", warnings)
	}
}

// TestValidateResolvedRepoPathNoWarning: an existing directory warns not.
func TestValidateResolvedRepoPathNoWarning(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{"a/b": {Path: t.TempDir()}}}
	warnings, err := doc.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("resolved path should not warn, got: %#v", warnings)
	}
}

// TestWithMergedSecretsKeepsWhenEmpty: an empty incoming key keeps the
// stored one (secret-merge).
func TestWithMergedSecretsKeepsWhenEmpty(t *testing.T) {
	prev := Document{Linear: LinearConfig{APIKey: "kept"}}
	incoming := Document{Linear: LinearConfig{APIKey: ""}}
	got := incoming.WithMergedSecrets(prev)
	if got.Linear.APIKey != "kept" {
		t.Errorf("empty incoming key should keep stored, got %q", got.Linear.APIKey)
	}
}

// TestWithMergedSecretsReplacesWhenSet: a non-empty incoming key replaces.
func TestWithMergedSecretsReplacesWhenSet(t *testing.T) {
	prev := Document{Linear: LinearConfig{APIKey: "old"}}
	incoming := Document{Linear: LinearConfig{APIKey: "new"}}
	got := incoming.WithMergedSecrets(prev)
	if got.Linear.APIKey != "new" {
		t.Errorf("non-empty incoming key should replace, got %q", got.Linear.APIKey)
	}
}

// TestWithMergedSecretsClears: an explicit clear drops the stored key even
// though the incoming key is empty (the redacted UI form always submits an
// empty key). Secret-merge alone would keep it, so clearing is a distinct
// signal (config-screen-spec). The signal itself must not persist.
func TestWithMergedSecretsClears(t *testing.T) {
	prev := Document{Linear: LinearConfig{APIKey: "kept"}}
	incoming := Document{Linear: LinearConfig{APIKey: "", ClearAPIKey: true}}
	got := incoming.WithMergedSecrets(prev)
	if got.Linear.APIKey != "" {
		t.Errorf("explicit clear should drop the stored key, got %q", got.Linear.APIKey)
	}
	if got.Linear.ClearAPIKey {
		t.Error("clear signal must not survive into the merged doc")
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "api_key_clear") {
		t.Errorf("clear signal leaked into the persisted doc: %s", data)
	}
}

// TestReposListSorted: ReposList projects repos to a name-sorted slice —
// the stable order the run-form dropdown reads (run-workflow-spec R2).
func TestReposListSorted(t *testing.T) {
	doc := Document{Repos: map[string]RepoConfig{
		"z/last":  {Path: "/z"},
		"a/first": {Path: "/a"},
		"m/mid":   {Path: "/m"},
	}}
	got := doc.ReposList()
	want := []RepoRef{
		{Name: "a/first", Path: "/a"},
		{Name: "m/mid", Path: "/m"},
		{Name: "z/last", Path: "/z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReposList = %#v, want %#v", got, want)
	}
}
