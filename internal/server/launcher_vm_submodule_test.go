package server

import (
	"os"
	"path/filepath"
	"testing"
)

// copyHostSubmodules fills a materialized workspace's submodule dirs from the
// HOST checkout: jj tracks only the gitlink, so `workspace add` leaves the
// submodule CONTENT absent (Ghost's default themes live in submodules — the app
// 500s without them). A submodule present in the host is copied (minus its own
// .git); one absent host-side is skipped without error (the guest fetches it);
// no .gitmodules is a no-op.
func TestCopyHostSubmodules(t *testing.T) {
	host := t.TempDir()
	// Host checkout has `themes/casper` populated (with its own .git, which must
	// NOT be copied) and does NOT have `themes/source` at all.
	casper := filepath.Join(host, "themes", "casper")
	mustWrite(t, filepath.Join(casper, "index.hbs"), "<html>casper</html>")
	mustWrite(t, filepath.Join(casper, ".git"), "gitdir: ../../.git/modules/themes/casper")
	mustWrite(t, filepath.Join(casper, "partials", "nav.hbs"), "nav")

	// The materialized workspace carries only the tracked .gitmodules, listing
	// both submodule paths; neither's content is present yet.
	work := t.TempDir()
	mustWrite(t, filepath.Join(work, ".gitmodules"), `[submodule "casper"]
	path = themes/casper
	url = ../casper.git
[submodule "source"]
	path = themes/source
	url = ../source.git
`)

	if err := copyHostSubmodules(host, work); err != nil {
		t.Fatalf("copyHostSubmodules: %v", err)
	}

	// Present submodule copied, including nested files.
	if got, err := os.ReadFile(filepath.Join(work, "themes", "casper", "index.hbs")); err != nil || string(got) != "<html>casper</html>" {
		t.Fatalf("themes/casper/index.hbs = %q (err %v); present submodule not copied", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(work, "themes", "casper", "partials", "nav.hbs")); err != nil || string(got) != "nav" {
		t.Fatalf("nested submodule file not copied: %q (err %v)", got, err)
	}
	// The submodule's own .git must not cross — the guest builds its own metadata.
	if _, err := os.Stat(filepath.Join(work, "themes", "casper", ".git")); !os.IsNotExist(err) {
		t.Errorf("submodule .git was copied into the workspace (err %v); must be skipped", err)
	}
	// Absent host-side → skipped, no error, no dir created.
	if _, err := os.Stat(filepath.Join(work, "themes", "source")); !os.IsNotExist(err) {
		t.Errorf("themes/source materialized (err %v); host lacks it, so it must be left for the guest fetch", err)
	}
}

// No .gitmodules in the workspace → nothing to do, no error.
func TestCopyHostSubmodulesNoGitmodules(t *testing.T) {
	if err := copyHostSubmodules(t.TempDir(), t.TempDir()); err != nil {
		t.Fatalf("no-.gitmodules must be a no-op, got %v", err)
	}
}

// An empty host submodule dir (a gitlink never checked out) is treated as
// absent: skipped without error, left for the guest's network fetch.
func TestCopyHostSubmodulesEmptyHostDir(t *testing.T) {
	host := t.TempDir()
	if err := os.MkdirAll(filepath.Join(host, "themes", "casper"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	mustWrite(t, filepath.Join(work, ".gitmodules"), "[submodule \"casper\"]\n\tpath = themes/casper\n")
	if err := copyHostSubmodules(host, work); err != nil {
		t.Fatalf("copyHostSubmodules: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "themes", "casper")); !os.IsNotExist(err) {
		t.Errorf("empty host submodule was materialized (err %v); must be skipped", err)
	}
}

// submodulePaths parses the `path =` entries and ignores everything else.
func TestSubmodulePaths(t *testing.T) {
	dir := t.TempDir()
	gm := filepath.Join(dir, ".gitmodules")
	mustWrite(t, gm, `[submodule "a"]
	path = ghost/core/content/themes/casper
	url = https://example.com/casper.git
	branch = main
[submodule "b"]
	url = https://example.com/source.git
	path = ghost/core/content/themes/source
`)
	paths, err := submodulePaths(gm)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ghost/core/content/themes/casper", "ghost/core/content/themes/source"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
	// Absent file → nil, no error.
	if p, err := submodulePaths(filepath.Join(dir, "nope")); err != nil || p != nil {
		t.Errorf("absent .gitmodules: paths=%v err=%v, want nil,nil", p, err)
	}
}
