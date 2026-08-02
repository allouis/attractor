package cli

import (
	"testing"

	"github.com/allouis/attractor/internal/config"
)

// TestServeReposLoadsConfig checks serve wires the central config.json
// through to items.Repos: an owner/name → path entry is projected and
// looked up, backing POST /items/run's repo → cwd resolution (items-spec
// I3/I4).
func TestServeReposLoadsConfig(t *testing.T) {
	home := t.TempDir()
	doc := config.Document{Repos: map[string]config.RepoConfig{
		"allouis/attractor": {Path: "/home/agent/attractor"},
	}}
	if err := doc.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}

	repos, err := serveRepos(home)
	if err != nil {
		t.Fatalf("serveRepos: %v", err)
	}
	if got, ok := repos.Path("allouis/attractor"); !ok || got != "/home/agent/attractor" {
		t.Errorf("repos.Path = %q, ok=%v; want /home/agent/attractor", got, ok)
	}
}
