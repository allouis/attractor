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
