package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/source"
)

// writeFile writes content to a fresh temp file named name and returns
// its absolute path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// markerChild returns a child pipeline .dot whose single tool node
// `touch`es marker — a filesystem trace of which child manager_loop ran.
func markerChild(t *testing.T, dir, name, marker string) string {
	t.Helper()
	src := fmt.Sprintf(`digraph child {
		start [shape=Mdiamond]
		mark [type="tool", tool_command="touch %s"]
		done [shape=Msquare]
		start -> mark -> done
	}`, marker)
	return writeFile(t, dir, name, src)
}

// TestRunItem_RouterRoutesPRToReviewChild is the R6 convention (router-spec
// "Testing conventions"): POST /items/run {item_ref, pipeline: router} for
// a PR Item runs the router, whose item.type=pr edge routes to the review
// child; the single run carries item_ref.
func TestRunItem_RouterRoutesPRToReviewChild(t *testing.T) {
	repoDir := t.TempDir()
	work := t.TempDir()
	reviewMarker := filepath.Join(work, "review.ran")
	implementMarker := filepath.Join(work, "implement.ran")
	reviewChild := markerChild(t, work, "review_child.dot", reviewMarker)
	implementChild := markerChild(t, work, "implement_child.dot", implementMarker)

	routerSrc := fmt.Sprintf(`digraph router {
		start [shape=Mdiamond]
		classify [type="conditional"]
		review_loop [
			shape=house,
			stack.child_dotfile="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		implement_loop [
			shape=house,
			stack.child_dotfile="%s",
			manager.poll_interval="10ms",
			manager.max_cycles=200
		]
		done [shape=Msquare]

		start -> classify
		classify -> review_loop    [condition="item.type=pr"]
		classify -> implement_loop [condition="item.type=issue"]
		review_loop    -> done
		implement_loop -> done
	}`, reviewChild, implementChild)
	routerPath := writeFile(t, work, "router.dot", routerSrc)

	fs := &fakeSource{getItem: prItem()}
	repos := config.Repos{"allouis/attractor": repoDir}
	srv := itemsServerWithRepos(t, map[string]source.Source{"github": fs}, repos)

	ref := engine.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	resp, id := postRunItem(t, srv.URL(), map[string]any{
		"item_ref": ref,
		"pipeline": routerPath,
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	summary := pollRunSummary(t, srv.URL(), id)
	if got, _ := summary["status"].(string); got != string(RunCompleted) {
		t.Fatalf("router run status = %q, want completed", got)
	}

	// item.type=pr routed to the review child, not the implement child.
	if _, err := os.Stat(reviewMarker); err != nil {
		t.Errorf("review child did not run (marker %s missing): %v", reviewMarker, err)
	}
	if _, err := os.Stat(implementMarker); err == nil {
		t.Errorf("implement child ran but should not have (item.type=pr)")
	}

	// One registry run per item-run, and it carries item_ref (router-spec
	// Shape: "One registry run per item-run (the router)").
	if summary["item_ref"] == nil {
		t.Error("router run summary missing item_ref")
	}
	if runs := srv.registry.RunsForItem(ref); len(runs) != 1 {
		t.Errorf("RunsForItem = %d runs, want 1 (single run carries item_ref)", len(runs))
	}
}
