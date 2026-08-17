package hub

import (
	"net/http/httptest"
	"testing"

	"github.com/allouis/attractor/internal/runserver"
)

// A token-protected run server rejects bare requests; the hub scrapes
// it with the token the announce carried (Phase 4: auth on the run
// server unlocks remote runners).
func TestHub_ScrapesTokenProtectedRun(t *testing.T) {
	dir := t.TempDir()
	writeRunDir(t, dir, "r1", "running")
	rs := runserver.New(dir)
	rs.Token = "s3cret"
	ts := httptest.NewServer(rs.Handler(true))
	t.Cleanup(ts.Close)

	// Bare request → 401.
	resp := postJSON(t, ts.URL+"/pipelines/r1", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("unauthenticated request got %d, want 401", resp.StatusCode)
	}

	_, hts := newHub(t)
	postJSON(t, hts.URL+"/announce", map[string]string{"run_id": "r1", "url": ts.URL, "token": "s3cret"})
	doc := getJSON[map[string]any](t, hts.URL+"/pipelines/r1")
	if doc["run_id"] != "r1" {
		t.Fatalf("hub could not scrape token-protected run: %+v", doc)
	}
}

func TestLauncher_LaunchArgs(t *testing.T) {
	l := Launcher{HubURL: "http://127.0.0.1:7690"}
	args := l.LaunchArgs(launchBody{
		Path: "bug-fix",
		Cwd:  "/work/repo",
		Vars: map[string]string{"repo": "o/r", "identifier": "X-1"},
	}, "/logs/run1")
	want := []string{
		"run", "--announce", "http://127.0.0.1:7690", "--logs", "/logs/run1",
		"--cwd", "/work/repo",
		"-var", "identifier=X-1", "-var", "repo=o/r",
		"bug-fix",
	}
	if len(args) != len(want) {
		t.Fatalf("args = %v, want %v", args, want)
	}
	for i := range want {
		if args[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full: %v)", i, args[i], want[i], args)
		}
	}
}
