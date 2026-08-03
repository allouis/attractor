package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/source"
)

var errTestGet = errors.New("gh: not found")

// runFormServer starts a server wired for the run form: a workflow catalog
// root, named sources, and a live-config repo registry — the three inputs
// POST /workflows/{name}/run resolves against (run-workflow-spec R3). Repos
// are written to config.json under a temp HOME, the same live source the run
// form's dropdown and the run's checks/placement read, so the test exercises
// the runtime-editable path rather than a boot snapshot.
func runFormServer(t *testing.T, catalog string, sources map[string]source.Source, repos items.Repos) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if len(repos) > 0 {
		doc := config.Document{Repos: map[string]config.RepoConfig{}}
		for name, path := range repos {
			doc.Repos[name] = config.RepoConfig{Path: path}
		}
		if err := doc.Save(home); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir(), WorkflowsDir: catalog, Sources: sources})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// writeVarsWorkflow creates <root>/<name>/pipeline.dot declaring vars.
func writeVarsWorkflow(t *testing.T, root, name, vars string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `digraph ` + name + ` {
		vars="` + vars + `"
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a -> done
	}`
	if err := os.WriteFile(filepath.Join(dir, "pipeline.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// postRunWorkflow POSTs POST /workflows/{name}/run and returns the response
// plus decoded id.
func postRunWorkflow(t *testing.T, url, name string, body map[string]any) (*http.Response, string) {
	t.Helper()
	buf, _ := json.Marshal(body)
	resp, err := http.Post(url+"/workflows/"+name+"/run", "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	return resp, out.ID
}

// TestRunWorkflow_StandaloneSeedsVarsRepoAndChecks proves a standalone launch
// (no item_ref) submits with the chosen repo as cwd, seeds the request vars
// and the repo var, and backfills the static checks to their no-op defaults.
func TestRunWorkflow_StandaloneSeedsVarsRepoAndChecks(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "implement", "repo,identifier")
	repoDir := t.TempDir()
	srv := runFormServer(t, catalog, nil, items.Repos{"allouis/attractor": repoDir})

	resp, id := postRunWorkflow(t, srv.URL(), "implement", map[string]any{
		"repo": "allouis/attractor",
		"vars": map[string]string{"identifier": "ENG-42"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if id == "" {
		t.Fatal("want a run id")
	}
	run, ok := srv.registry.Get(id)
	if !ok {
		t.Fatalf("run %q not registered", id)
	}
	if got := run.initialContext["repo"]; got != "allouis/attractor" {
		t.Errorf("seeded repo = %q, want allouis/attractor", got)
	}
	if got := run.initialContext["identifier"]; got != "ENG-42" {
		t.Errorf("seeded identifier = %q, want ENG-42", got)
	}
	if run.initialContext["check.test"] == "" {
		t.Error("check.test not seeded (want no-op default)")
	}
	if run.workflowName != "implement" {
		t.Errorf("workflowName = %q, want implement", run.workflowName)
	}
	if run.repo != "allouis/attractor" {
		t.Errorf("run repo = %q, want allouis/attractor", run.repo)
	}
	if run.cwd != repoDir {
		t.Errorf("run cwd = %q, want %q", run.cwd, repoDir)
	}
	pollRunSummary(t, srv.URL(), id) // let the run finish before tempdir cleanup
}

// TestRunWorkflow_ItemRefPrefillsRequestOverlays proves an item-driven launch
// prefills each var from the resolved item, the request vars overlay them, the
// item's repo fills in when the request omits one, and the run is stamped with
// the item tag and its item.* metadata.
func TestRunWorkflow_ItemRefPrefillsRequestOverlays(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "review-pr", "repo,pr_number,title")
	repoDir := t.TempDir()
	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	fs := &fakeSource{getItem: source.Item{
		Ref:   ref,
		Title: "Fix login",
		Vars: map[string]string{
			"repo":      "allouis/attractor",
			"pr_number": "42",
			"title":     "old title",
		},
	}}
	srv := runFormServer(t, catalog, map[string]source.Source{"github": fs}, items.Repos{"allouis/attractor": repoDir})

	resp, id := postRunWorkflow(t, srv.URL(), "review-pr", map[string]any{
		"item_ref": ref,
		"vars":     map[string]string{"title": "new title"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if fs.gotRef != ref {
		t.Errorf("source.Get ref = %+v, want %+v", fs.gotRef, ref)
	}
	run, ok := srv.registry.Get(id)
	if !ok {
		t.Fatalf("run %q not registered", id)
	}
	if got := run.initialContext["repo"]; got != "allouis/attractor" {
		t.Errorf("seeded repo = %q, want allouis/attractor (item prefill)", got)
	}
	if got := run.initialContext["pr_number"]; got != "42" {
		t.Errorf("seeded pr_number = %q, want 42 (item prefill)", got)
	}
	if got := run.initialContext["title"]; got != "new title" {
		t.Errorf("seeded title = %q, want \"new title\" (request overlay)", got)
	}
	if got := run.initialContext["item.type"]; got != "pr" {
		t.Errorf("seeded item.type = %q, want pr", got)
	}
	if run.itemRef != ref.String() {
		t.Errorf("run itemRef = %q, want %q", run.itemRef, ref.String())
	}
	if run.cwd != repoDir {
		t.Errorf("run cwd = %q, want %q (repo from item)", run.cwd, repoDir)
	}
	pollRunSummary(t, srv.URL(), id) // let the run finish before tempdir cleanup
}

// TestRunWorkflow_BaseDirIsWorkflowDir proves the run's baseDir is the catalog
// workflow's own directory, so an @file prompt authored relative to the
// pipeline resolves — even though the work cwd is the (prompt-less) repo.
func TestRunWorkflow_BaseDirIsWorkflowDir(t *testing.T) {
	catalog := t.TempDir()
	dir := filepath.Join(catalog, "implement")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `digraph implement {
		vars="repo"
		start [shape=Mdiamond]
		a [prompt="@task.md"]
		done [shape=Msquare]
		start -> a -> done
	}`
	if err := os.WriteFile(filepath.Join(dir, "pipeline.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "task.md"), []byte("do the thing"), 0o644); err != nil {
		t.Fatal(err)
	}
	repoDir := t.TempDir() // no task.md here — resolving against cwd would fail
	srv := runFormServer(t, catalog, nil, items.Repos{"allouis/attractor": repoDir})

	resp, id := postRunWorkflow(t, srv.URL(), "implement", map[string]any{"repo": "allouis/attractor"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (@task.md must resolve against the workflow dir)", resp.StatusCode)
	}
	pollRunSummary(t, srv.URL(), id) // let the run finish before tempdir cleanup
}

// TestRunWorkflow_ResolvesRepoAddedAfterBoot proves the run form resolves cwd
// from live config, not a boot snapshot: a repo registered at runtime (via PUT
// /config, the config screen's path) is immediately runnable. Guards the
// split-brain where the dropdown (live) offered a repo the run rejected as
// unmapped (snapshot) until a daemon restart.
func TestRunWorkflow_ResolvesRepoAddedAfterBoot(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "implement", "repo")
	srv := runFormServer(t, catalog, nil, nil) // boots with no repos

	repoDir := t.TempDir()
	body, _ := json.Marshal(config.Document{
		Repos: map[string]config.RepoConfig{"allouis/attractor": {Path: repoDir}},
	})
	req, _ := http.NewRequest(http.MethodPut, srv.URL()+"/config", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	putResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /config status = %d, want 200", putResp.StatusCode)
	}
	putResp.Body.Close()

	runResp, id := postRunWorkflow(t, srv.URL(), "implement", map[string]any{"repo": "allouis/attractor"})
	if runResp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (repo added after boot must be runnable)", runResp.StatusCode)
	}
	run, ok := srv.registry.Get(id)
	if !ok {
		t.Fatalf("run %q not registered", id)
	}
	if run.cwd != repoDir {
		t.Errorf("run cwd = %q, want %q (resolved from live config)", run.cwd, repoDir)
	}
	pollRunSummary(t, srv.URL(), id)
}

func TestRunWorkflow_UnknownWorkflow404(t *testing.T) {
	srv := runFormServer(t, t.TempDir(), nil, items.Repos{})
	resp, _ := postRunWorkflow(t, srv.URL(), "ghost", map[string]any{"repo": "allouis/attractor"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRunWorkflow_RepoUnmapped422(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "implement", "repo")
	srv := runFormServer(t, catalog, nil, items.Repos{})
	resp, _ := postRunWorkflow(t, srv.URL(), "implement", map[string]any{"repo": "allouis/attractor"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestRunWorkflow_NoRepo422(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "implement", "repo")
	srv := runFormServer(t, catalog, nil, items.Repos{})
	resp, _ := postRunWorkflow(t, srv.URL(), "implement", map[string]any{})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
}

func TestRunWorkflow_UnknownSource400(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "review-pr", "repo")
	srv := runFormServer(t, catalog, nil, items.Repos{"allouis/attractor": t.TempDir()})
	resp, _ := postRunWorkflow(t, srv.URL(), "review-pr", map[string]any{
		"item_ref": items.ItemRef{Source: "nope", Type: "pr", ExternalID: "x#1"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestRunWorkflow_ItemGetFails502(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "review-pr", "repo")
	fs := &fakeSource{getErr: errTestGet}
	srv := runFormServer(t, catalog, map[string]source.Source{"github": fs}, items.Repos{"allouis/attractor": t.TempDir()})
	resp, _ := postRunWorkflow(t, srv.URL(), "review-pr", map[string]any{
		"item_ref": items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"},
	})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestRunWorkflow_BadItemRef400(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "review-pr", "repo")
	srv := runFormServer(t, catalog, map[string]source.Source{"github": &fakeSource{}}, items.Repos{})
	resp, _ := postRunWorkflow(t, srv.URL(), "review-pr", map[string]any{
		"item_ref": map[string]any{"source": "github", "type": "pr"}, // missing external_id
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
