package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestServeReposLoadsCwdConfig checks serve wires repos.toml through
// items.LoadRepos: an owner/name → path entry under cwd/.attractor is
// picked up, backing POST /items/run's repo → cwd resolution (items-spec
// I3/I4).
func TestServeReposLoadsCwdConfig(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".attractor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "[repos]\n\"allouis/attractor\" = \"/home/agent/attractor\"\n"
	if err := os.WriteFile(filepath.Join(dir, "repos.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}

	repos, err := serveRepos(home, cwd)
	if err != nil {
		t.Fatalf("serveRepos: %v", err)
	}
	if got, ok := repos.Path("allouis/attractor"); !ok || got != "/home/agent/attractor" {
		t.Errorf("repos.Path = %q, ok=%v; want /home/agent/attractor", got, ok)
	}
}
