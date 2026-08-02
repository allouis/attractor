package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/allouis/attractor/internal/config"
)

// TestGetReposProjectsConfig: GET /repos returns the registered repos as a
// name-sorted [{name, path}] list — the run-form dropdown source
// (run-workflow-spec R2), read live off config.repos.
func TestGetReposProjectsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustNil(t, config.Document{Repos: map[string]config.RepoConfig{
		"z/last":  {Path: "/z"},
		"a/first": {Path: "/a"},
	}}.Save(home))

	rec := serveConfig(t, http.MethodGet, "/repos", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got []config.RepoRef
	mustNil(t, json.Unmarshal(rec.Body.Bytes(), &got))
	want := []config.RepoRef{{Name: "a/first", Path: "/a"}, {Name: "z/last", Path: "/z"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("GET /repos = %#v, want %#v", got, want)
	}
}

// TestGetReposEmpty: no registered repos → an empty JSON array.
func TestGetReposEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rec := serveConfig(t, http.MethodGet, "/repos", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []config.RepoRef
	mustNil(t, json.Unmarshal(rec.Body.Bytes(), &got))
	if len(got) != 0 {
		t.Errorf("want empty list, got %#v", got)
	}
}

// TestGetReposReflectsEdits: a re-saved config is served without a restart
// (live read).
func TestGetReposReflectsEdits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	mustNil(t, config.Document{Repos: map[string]config.RepoConfig{"a/one": {Path: "/a"}}}.Save(home))
	first := serveConfig(t, http.MethodGet, "/repos", nil)
	var got1 []config.RepoRef
	mustNil(t, json.Unmarshal(first.Body.Bytes(), &got1))
	if len(got1) != 1 {
		t.Fatalf("want one repo initially, got %#v", got1)
	}

	mustNil(t, config.Document{Repos: map[string]config.RepoConfig{
		"a/one": {Path: "/a"}, "b/two": {Path: "/b"},
	}}.Save(home))
	second := serveConfig(t, http.MethodGet, "/repos", nil)
	var got2 []config.RepoRef
	mustNil(t, json.Unmarshal(second.Body.Bytes(), &got2))
	if len(got2) != 2 {
		t.Errorf("live read should reflect the edit, got %#v", got2)
	}
}
