package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/source"
)

// TestWorkflowCatalogPath_DrivesItemRun proves the compose the Items
// run-action picker relies on (web-ui-spec W4): a path from the GET
// /workflows catalog is a valid `pipeline` for POST /items/run. It lists
// the catalog, dispatches the first entry's path for a PR item, and checks
// the run completes and carries item_ref.
func TestWorkflowCatalogPath_DrivesItemRun(t *testing.T) {
	root := t.TempDir()
	repoDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	dir := filepath.Join(root, "alpha")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`digraph alpha {
		start [shape=Mdiamond]
		mark [type="tool", tool_command="touch %s"]
		done [shape=Msquare]
		start -> mark -> done
	}`, marker)
	if err := os.WriteFile(filepath.Join(dir, "pipeline.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := &fakeSource{getItem: prItem()}
	srv := New(Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     t.TempDir(),
		Sources:      map[string]source.Source{"github": fs},
		Repos:        items.Repos{"allouis/attractor": repoDir},
		WorkflowsDir: root,
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// The catalog path the picker would hand to the run action.
	resp, err := http.Get(srv.URL() + "/workflows")
	if err != nil {
		t.Fatal(err)
	}
	var catalog []workflow
	_ = json.NewDecoder(resp.Body).Decode(&catalog)
	resp.Body.Close()
	if len(catalog) != 1 {
		t.Fatalf("catalog = %d entries, want 1", len(catalog))
	}

	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	runResp, id := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": ref,
		"pipeline": catalog[0].Path,
	})
	if runResp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", runResp.StatusCode)
	}

	summary := pollRunSummary(t, srv.URL(), id)
	if got, _ := summary["status"].(string); got != string(RunCompleted) {
		t.Fatalf("run status = %q, want completed", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("catalog workflow did not run (marker %s missing): %v", marker, err)
	}
	if summary["item_ref"] == nil {
		t.Error("run summary missing item_ref")
	}
	if runs := srv.registry.RunsForItem(ref.String()); len(runs) != 1 {
		t.Errorf("RunsForItem = %d, want 1", len(runs))
	}
}

// TestItemRun_StampsWorkflowName proves a run dispatched from the catalog
// carries workflow_name = the catalog directory name (web-ui-spec W6): the
// handle the run→workflow backlink targets (#workflow/<dir>). It is the dir
// name, NOT the DOT digraph name — graphviz ids cannot contain hyphens, so a
// `bug-fix/` dir holds a `digraph bug_fix`. Linking by graph_name would 404;
// only the dir name resolves against GET /workflows/{name}/graph.
func TestItemRun_StampsWorkflowName(t *testing.T) {
	root := t.TempDir()
	repoDir := t.TempDir()
	dir := filepath.Join(root, "bug-fix")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `digraph bug_fix {
		start [shape=Mdiamond]
		done [shape=Msquare]
		start -> done
	}`
	if err := os.WriteFile(filepath.Join(dir, "pipeline.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := &fakeSource{getItem: prItem()}
	srv := New(Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     t.TempDir(),
		Sources:      map[string]source.Source{"github": fs},
		Repos:        items.Repos{"allouis/attractor": repoDir},
		WorkflowsDir: root,
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	runResp, id := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": ref,
		"pipeline": filepath.Join(dir, "pipeline.dot"),
	})
	if runResp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", runResp.StatusCode)
	}
	summary := pollRunSummary(t, srv.URL(), id)
	if got := summary["workflow_name"]; got != "bug-fix" {
		t.Errorf("workflow_name = %v, want \"bug-fix\" (catalog dir, not digraph name)", got)
	}
	if got := summary["graph_name"]; got != "bug_fix" {
		t.Errorf("graph_name = %v, want \"bug_fix\" (sanity: digraph name differs from dir)", got)
	}
}
