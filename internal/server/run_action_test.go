package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/source"
)

// TestItemRun_DrivesRunToCompletion proves the item-driven launch the Items
// run action performs (run-workflow-spec R4): POST /workflows/{name}/run with
// an item_ref lists off the catalog, resolves the item's vars/repo, runs the
// workflow to completion, and stamps the run with item_ref. A tool node's
// marker file is the filesystem trace that the workflow actually executed.
func TestItemRun_DrivesRunToCompletion(t *testing.T) {
	catalog := t.TempDir()
	repoDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ran")
	dir := filepath.Join(catalog, "alpha")
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
	srv := runFormServer(t, catalog, map[string]source.Source{"github": fs}, items.Repos{"allouis/attractor": repoDir})

	// The catalog entry the run action would dispatch (keyed by name, not path).
	resp, err := http.Get(srv.URL() + "/workflows")
	if err != nil {
		t.Fatal(err)
	}
	var catalogList []workflow
	_ = json.NewDecoder(resp.Body).Decode(&catalogList)
	resp.Body.Close()
	if len(catalogList) != 1 {
		t.Fatalf("catalog = %d entries, want 1", len(catalogList))
	}

	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	runResp, id := postRunWorkflow(t, srv.URL(), catalogList[0].Name, map[string]any{
		"item_ref": ref,
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

// TestItemRun_StampsWorkflowName proves an item-driven run carries
// workflow_name = the catalog directory name (web-ui-spec W6): the handle the
// run→workflow backlink targets (#workflow/<dir>). It is the dir name, NOT the
// DOT digraph name — graphviz ids cannot contain hyphens, so a `bug-fix/` dir
// holds a `digraph bug_fix`. Linking by graph_name would 404; only the dir
// name resolves against GET /workflows/{name}/graph.
func TestItemRun_StampsWorkflowName(t *testing.T) {
	catalog := t.TempDir()
	repoDir := t.TempDir()
	dir := filepath.Join(catalog, "bug-fix")
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
	srv := runFormServer(t, catalog, map[string]source.Source{"github": fs}, items.Repos{"allouis/attractor": repoDir})

	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	runResp, id := postRunWorkflow(t, srv.URL(), "bug-fix", map[string]any{
		"item_ref": ref,
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

// TestItemsRunEndpointRemoved guards the migration: POST /items/run is gone,
// superseded by POST /workflows/{name}/run (run-workflow-spec R4). The mux has
// no such route, so it 404s rather than dispatching.
func TestItemsRunEndpointRemoved(t *testing.T) {
	srv := runFormServer(t, t.TempDir(), map[string]source.Source{"github": &fakeSource{}}, items.Repos{})
	resp, err := http.Post(srv.URL()+"/items/run", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST /items/run status = %d, want 404 (endpoint removed)", resp.StatusCode)
	}
}
