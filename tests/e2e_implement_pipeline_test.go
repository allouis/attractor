package attractor_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/lint"
)

// implementPipelineSrc reads the shipped implement pipeline (router-spec R6).
func implementPipelineSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../pipelines/implement/pipeline.dot")
	must(t, err)
	return string(b)
}

func buildImplementGraph(t *testing.T) *graphpkg.Graph {
	t.Helper()
	return build(t, implementPipelineSrc(t))
}

// TestImplementPipeline_LintsClean confirms the shipped implement
// pipeline — the router's `issue` target (router-spec R6) — is
// structurally valid: it parses, builds, and lints with no ERROR.
func TestImplementPipeline_LintsClean(t *testing.T) {
	g := buildImplementGraph(t)
	diags, err := lint.ValidateOrError(g)
	if err != nil {
		t.Fatalf("implement pipeline rejected: %v", err)
	}
	for _, d := range diags {
		if d.Severity == lint.Error {
			t.Fatalf("unexpected ERROR: %+v", d)
		}
	}
}

// TestImplementPipeline_SelfReviewGate pins the RV4 contract: after the
// agent implements, a `stack.manager_loop` runs the shared review-core
// sub-pipeline over the agent's own uncommitted work (diff_cmd = `jj
// diff`); a FAIL verdict routes back to `implement`, a PASS verdict exits.
func TestImplementPipeline_SelfReviewGate(t *testing.T) {
	g := buildImplementGraph(t)

	// start -> implement (the codergen build stage).
	out := g.OutgoingEdges("start")
	if len(out) != 1 {
		t.Fatalf("start should have exactly one outgoing edge, got %d", len(out))
	}
	implementID := out[0].To
	if impl := g.Nodes[implementID]; impl.Type() != "codergen" {
		t.Fatalf("first stage %q type=%q, want codergen", implementID, impl.Type())
	}

	// implement -> review_loop (the self-review gate).
	io := g.OutgoingEdges(implementID)
	if len(io) != 1 {
		t.Fatalf("implement should have exactly one outgoing edge, got %d", len(io))
	}
	loopID := io[0].To
	loop := g.Nodes[loopID]
	if loop.Type() != "stack.manager_loop" {
		t.Fatalf("review stage %q type=%q, want stack.manager_loop", loopID, loop.Type())
	}
	if child := loop.Attrs["stack.child_dotfile"]; !strings.HasSuffix(child, "review-core/pipeline.dot") {
		t.Fatalf("review_loop child_dotfile=%q, want suffix review-core/pipeline.dot", child)
	}
	if diffCmd := loop.Attrs["stack.child.var.diff_cmd"]; !strings.Contains(diffCmd, "jj diff") {
		t.Fatalf("review_loop diff_cmd=%q, want it to contain `jj diff`", diffCmd)
	}

	// review_loop branches on the verdict: FAIL -> implement, PASS -> exit.
	var backToImplement, toExit bool
	for _, e := range g.OutgoingEdges(loopID) {
		cond := e.Attrs["condition"]
		switch {
		case strings.Contains(cond, "outcome=fail") && e.To == implementID:
			backToImplement = true
		case strings.Contains(cond, "outcome=success") && g.Nodes[e.To].Type() == "exit":
			toExit = true
		}
	}
	if !backToImplement {
		t.Fatal("review_loop should route back to implement on outcome=fail")
	}
	if !toExit {
		t.Fatal("review_loop should route to exit on outcome=success")
	}
}

// TestImplementPipeline_FailRoutesBackThenPasses drives implement.dot end
// to end (RV4): the first self-review returns a FAIL verdict, routing back
// to `implement`; the second review passes and the run exits SUCCESS. The
// child_dotfile is the real review-core pipeline via an absolute path (the
// shipped relative path resolves against the pipeline's own dir, not the
// test cwd).
func TestImplementPipeline_FailRoutesBackThenPasses(t *testing.T) {
	childPath, err := filepath.Abs("../pipelines/review-core/pipeline.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph implement {
		vars = "repo,identifier,url,title"
		max_node_visits = 3
		start [shape=Mdiamond]
		implement [prompt="build the change"]
		review_loop [type="stack.manager_loop",
		             stack.child_dotfile="%s",
		             stack.child.var.diff_cmd="jj diff",
		             manager.poll_interval="10ms",
		             manager.max_cycles=2000]
		done [shape=Msquare]
		start -> implement
		implement -> review_loop
		review_loop -> implement [condition="outcome=fail"]
		review_loop -> done      [condition="outcome=success"]
	}`, childPath)

	be := fake.New()
	be.SetText("implement", "implemented the change")
	for _, lens := range reviewCoreLenses {
		be.SetText(lens, "finding from "+lens)
	}
	// First review blocks (FAIL -> back to implement); second passes.
	be.SetSequence("synth",
		fake.Step{Outcome: &engine.Outcome{
			Status:        engine.StatusFail,
			FailureReason: "correctness lens found a blocking bug",
			ContextUpdates: map[string]string{
				"review.verdict": "fail",
			},
		}},
		fake.Step{Outcome: &engine.Outcome{
			Status: engine.StatusSuccess,
			Notes:  "merged review",
			ContextUpdates: map[string]string{
				"review.summary": "all lenses clear",
				"review.verdict": "pass",
			},
		}},
	)

	out, _, _ := runFixtureSeeded(t, src, be, nil, map[string]string{
		"repo":       "owner/repo",
		"identifier": "ENG-42",
		"url":        "https://linear.app/eng-42",
		"title":      "Fix login",
	})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}

	// The FAIL verdict routed back to implement, which re-ran; the second
	// review's synth then passed and the run exited. implement runs twice
	// (initial + after the blocking review), synth runs twice (the two
	// verdicts).
	if n := be.CallCount("implement"); n != 2 {
		t.Fatalf("implement called %d times, want 2 (fail routed back)", n)
	}
	if n := be.CallCount("synth"); n != 2 {
		t.Fatalf("synth called %d times, want 2 (fail then pass)", n)
	}
	// The first review fanned out to every lens over the agent's own
	// uncommitted work: diff_cmd = jj diff.
	for _, lens := range reviewCoreLenses {
		if be.CallCount(lens) < 1 {
			t.Fatalf("lens %q never ran, want the self-review to fan out to it", lens)
		}
	}
	if got := childPrompt(t, be, "correctness"); !strings.Contains(got, "jj diff") {
		t.Fatalf("lens prompt = %q, want it to contain `jj diff`", got)
	}
}
