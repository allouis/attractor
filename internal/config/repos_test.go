package config

import "testing"

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
