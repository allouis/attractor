package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// P2.8 (D3, spec amendment A1): each visit to a node writes its
// artifacts under {node_id}/v{N}/; the latest visit is mirrored at the
// node root so spec §5.6 consumers keep working. Opening a historical
// visit and reading its prompt/response is how failed loops get
// debugged.
func TestVisitDirs_PerVisitArtifactsAndRootMirror(t *testing.T) {
	src := `digraph t {
		max_node_visits=5
		max_repeated_failures=0
		start [shape=Mdiamond]
		work [prompt="attempt the work"]
		fix  [prompt="fix it"]
		done [shape=Msquare]
		start -> work
		work -> done [condition="outcome=success"]
		work -> fix  [condition="outcome=fail"]
		fix -> work
	}`
	be := fake.New()
	calls := 0
	wrapped := backend.Func(func(env engine.HandlerEnv, prompt string) (backend.Result, error) {
		if env.Node.ID != "work" {
			return be.Run(env, prompt)
		}
		calls++
		if calls == 1 {
			return backend.Result{Outcome: &engine.Outcome{
				Status: engine.StatusFail, FailureReason: "first try fails",
			}, ResponseText: "first response"}, nil
		}
		return backend.Result{ResponseText: "second response"}, nil
	})

	out, _, logs := runFixture(t, src, wrapped, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %+v", out)
	}

	// Both visits' artifacts exist under v1/v2.
	v1, err := os.ReadFile(filepath.Join(logs, "work", "v1", "prompt.md"))
	if err != nil {
		t.Fatalf("v1 prompt missing: %v", err)
	}
	if !strings.Contains(string(v1), "attempt the work") {
		t.Fatalf("v1 prompt content wrong: %s", v1)
	}
	v2resp, err := os.ReadFile(filepath.Join(logs, "work", "v2", "response.md"))
	if err != nil {
		t.Fatalf("v2 response missing: %v", err)
	}
	if string(v2resp) != "second response" {
		t.Fatalf("v2 response = %q", v2resp)
	}

	// The node root mirrors the LATEST visit (spec §5.6 compatibility).
	rootResp, err := os.ReadFile(filepath.Join(logs, "work", "response.md"))
	if err != nil {
		t.Fatalf("root mirror missing: %v", err)
	}
	if string(rootResp) != "second response" {
		t.Fatalf("root mirror = %q, want latest visit's response", rootResp)
	}
	rootStatus, err := os.ReadFile(filepath.Join(logs, "work", "status.json"))
	if err != nil {
		t.Fatalf("root status.json missing: %v", err)
	}
	if !strings.Contains(string(rootStatus), "success") {
		t.Fatalf("root status.json = %s", rootStatus)
	}
}

// A single-visit node keeps the same layout: v1 plus the mirror.
func TestVisitDirs_SingleVisit(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	be := fake.New()
	be.SetText("work", "hello")
	out, _, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(logs, "work", "v1", "response.md")); err != nil {
		t.Fatalf("v1 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logs, "work", "response.md")); err != nil {
		t.Fatalf("root mirror missing: %v", err)
	}
}
