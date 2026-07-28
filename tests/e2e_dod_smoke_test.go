package attractor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/dot"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
)

// TestDoD_AttractorSection_11_13 exercises the integration smoke test
// described in Attractor spec §11.13. It verifies parse, validate, run,
// artefact production, goal-gate satisfaction, and checkpoint state for
// the canonical hello-world pipeline.
func TestDoD_AttractorSection_11_13(t *testing.T) {
	src, err := os.ReadFile("../testdata/pipelines/smoke.dot")
	must(t, err)

	// 1. Parse
	file, err := dot.Parse(string(src))
	must(t, err)
	g, err := graphpkg.Build(file)
	must(t, err)
	if got := g.Goal(); got != "Create a hello world Python script" {
		t.Fatalf("graph goal=%q", got)
	}
	if n := len(g.Nodes); n != 5 {
		t.Fatalf("graph nodes = %d, want 5 (got %v)", n, g.NodeOrder)
	}
	if n := len(g.Edges); n != 6 {
		t.Fatalf("graph edges = %d, want 6", n)
	}

	// 2. Validate
	prepared, err := engine.Prepare(g)
	must(t, err)
	_ = prepared

	// 3. Execute with the FakeBackend.
	be := fake.New()
	be.SetText("plan", "PLAN: write print('hello world')")
	be.SetText("implement", "IMPL: print('hello world')")
	be.SetText("review", "REVIEW: lgtm")
	outcome, _, logs := runFixture(t, string(src), be, nil)

	// 4. Verify outcome.
	if outcome.Status != engine.StatusSuccess {
		t.Fatalf("expected SUCCESS, got %s reason=%q", outcome.Status, outcome.FailureReason)
	}

	// 5. Each codergen node produced full artefact set.
	for _, id := range []string{"plan", "implement", "review"} {
		dir := filepath.Join(logs, id)
		for _, f := range []string{"prompt.md", "response.md", "status.json"} {
			path := filepath.Join(dir, f)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("missing %s: %v", path, err)
			}
		}
	}

	// 6. Goal gate satisfied — implement reached SUCCESS.
	st := readStatus(t, logs, "implement")
	if st.Status != engine.StatusSuccess {
		t.Fatalf("implement.status = %s; goal_gate would block exit", st.Status)
	}

	// 7. Checkpoint reflects completion of all three codergen nodes.
	data, err := os.ReadFile(filepath.Join(logs, "checkpoint.json"))
	must(t, err)
	var ck struct {
		CompletedNodes []string `json:"completed_nodes"`
	}
	must(t, json.Unmarshal(data, &ck))
	expectAll := []string{"plan", "implement", "review"}
	for _, want := range expectAll {
		found := false
		for _, got := range ck.CompletedNodes {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("checkpoint missing %q (got %v)", want, ck.CompletedNodes)
		}
	}

	// 8. $goal expansion landed in the plan prompt.
	prompt, err := os.ReadFile(filepath.Join(logs, "plan", "prompt.md"))
	must(t, err)
	if !strings.Contains(string(prompt), "Create a hello world Python script") {
		t.Fatalf("plan prompt missing $goal expansion: %q", prompt)
	}
}

// TestDoD_RetryAndFailEdgeRouting covers the §11.13 expected behaviour
// where `implement` failing routes back to `plan` until success.
func TestDoD_RetryAndFailEdgeRouting(t *testing.T) {
	src, err := os.ReadFile("../testdata/pipelines/smoke.dot")
	must(t, err)
	be := fake.New()
	be.SetSequence("plan",
		fake.Step{Text: "plan v1"},
		fake.Step{Text: "plan v2"},
	)
	be.SetSequence("implement",
		fake.Step{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: "scaffolding broken"}},
		fake.Step{Text: "impl ok"},
	)
	be.SetText("review", "lgtm")

	outcome, _, _ := runFixture(t, string(src), be, nil)
	if outcome.Status != engine.StatusSuccess {
		t.Fatalf("expected SUCCESS after retry path, got %s reason=%q", outcome.Status, outcome.FailureReason)
	}
	if be.CallCount("plan") != 2 {
		t.Fatalf("plan should run twice (initial + retry), got %d", be.CallCount("plan"))
	}
	if be.CallCount("implement") != 2 {
		t.Fatalf("implement should run twice, got %d", be.CallCount("implement"))
	}
}
