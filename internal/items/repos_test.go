package items

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseReposBasic captures the [repos] table shape from items-spec I3:
// `owner/name = "/local/checkout"` entries, looked up via Path.
func TestParseReposBasic(t *testing.T) {
	repos, err := ParseRepos([]byte("[repos]\n\"allouis/attractor\" = \"/home/agent/attractor\"\n"))
	if err != nil {
		t.Fatalf("ParseRepos: %v", err)
	}
	got, ok := repos.Path("allouis/attractor")
	if !ok || got != "/home/agent/attractor" {
		t.Errorf("Path(allouis/attractor) = %q, %v; want %q, true", got, ok, "/home/agent/attractor")
	}
}

// TestParseReposUnquotedKey checks the lenient parser accepts a bare
// owner/name key (the `/` is fine in Attractor's tiny TOML subset).
func TestParseReposUnquotedKey(t *testing.T) {
	repos, err := ParseRepos([]byte("[repos]\nallouis/attractor = \"/p\"\n"))
	if err != nil {
		t.Fatalf("ParseRepos: %v", err)
	}
	if got, ok := repos.Path("allouis/attractor"); !ok || got != "/p" {
		t.Errorf("Path(allouis/attractor) = %q, %v; want %q, true", got, ok, "/p")
	}
}

// TestParseReposUnknownRepo: a miss returns ok=false.
func TestParseReposUnknownRepo(t *testing.T) {
	repos, err := ParseRepos([]byte("[repos]\na/b = \"/p\"\n"))
	if err != nil {
		t.Fatalf("ParseRepos: %v", err)
	}
	if got, ok := repos.Path("nope/x"); ok {
		t.Errorf("Path(nope/x) = %q, %v; want \"\", false", got, ok)
	}
}

// TestParseReposIgnoresOtherTables: keys outside [repos] are ignored, so a
// stray table doesn't leak into the map.
func TestParseReposIgnoresOtherTables(t *testing.T) {
	repos, err := ParseRepos([]byte("[providers.foo]\nbackend = \"acp\"\n[repos]\na/b = \"/p\"\n"))
	if err != nil {
		t.Fatalf("ParseRepos: %v", err)
	}
	if _, ok := repos.Path("backend"); ok {
		t.Errorf("leaked non-repos key into map")
	}
	if _, ok := repos.Path("a/b"); !ok {
		t.Errorf("Path(a/b) missing; got %v", repos)
	}
}

// TestParseReposMalformed: a non-header line without `=` is a line-numbered
// error, mirroring Parse.
func TestParseReposMalformed(t *testing.T) {
	if _, err := ParseRepos([]byte("[repos]\ngarbage\n")); err == nil {
		t.Fatalf("ParseRepos: want error on malformed line, got nil")
	}
}

// TestLoadReposMissingFilesEmpty: absent repos.toml yields an empty map,
// not an error (mirrors Load).
func TestLoadReposMissingFilesEmpty(t *testing.T) {
	repos, err := LoadRepos(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("LoadRepos: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("LoadRepos = %v, want empty", repos)
	}
}

// TestLoadReposOverlay: home then cwd, cwd winning per key; the union of
// keys survives.
func TestLoadReposOverlay(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	writeRepos(t, filepath.Join(home, ".attractor", "repos.toml"),
		"[repos]\na/b = \"/home\"\nc/d = \"/home2\"\n")
	writeRepos(t, filepath.Join(cwd, ".attractor", "repos.toml"),
		"[repos]\na/b = \"/cwd\"\n")
	repos, err := LoadRepos(home, cwd)
	if err != nil {
		t.Fatalf("LoadRepos: %v", err)
	}
	if got, _ := repos.Path("a/b"); got != "/cwd" {
		t.Errorf("Path(a/b) = %q, want /cwd (cwd wins)", got)
	}
	if got, _ := repos.Path("c/d"); got != "/home2" {
		t.Errorf("Path(c/d) = %q, want /home2 (home-only survives)", got)
	}
}

func writeRepos(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
