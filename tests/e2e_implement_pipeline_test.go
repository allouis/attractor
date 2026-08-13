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
	"github.com/allouis/attractor/internal/setup"
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

// TestImplementPipeline_SelfReviewGate pins the RV4 contract plus the
// plan-gate and publish extensions: a plan stage is human-approved before
// implementation; after the agent implements, a `stack.manager_loop` runs
// the shared review-core sub-pipeline over everything since the plan's
// review_base (diff_cmd = `jj diff …`); a FAIL verdict routes back to
// `implement`; a PASS verdict reaches the human ship gate, which publishes
// via a draft-PR node before exit.
func TestImplementPipeline_SelfReviewGate(t *testing.T) {
	g := buildImplementGraph(t)

	// start -> baseline_* (tool chain proving the starting point is green)
	// -> plan (codergen) -> plan_gate (wait.human): a human approves the
	// plan before any code is written, and can send it back to plan.
	cursor := g.OutgoingEdges("start")[0].To
	baselines := 0
	baselineIDs := map[string]bool{}
	for g.Nodes[cursor].Type() == "tool" {
		baselineIDs[cursor] = true
		if !strings.Contains(g.Nodes[cursor].Attrs["tool_command"], "$context.check.") {
			t.Fatalf("baseline node %q does not run a configured check", cursor)
		}
		baselines++
		cursor = g.OutgoingEdges(cursor)[0].To
	}
	if baselines < 4 {
		t.Fatalf("want >=4 baseline check nodes before plan, got %d", baselines)
	}
	planID := cursor
	if p := g.Nodes[planID]; p.Type() != "codergen" {
		t.Fatalf("post-baseline stage %q type=%q, want codergen", planID, p.Type())
	}
	var gateID string
	for _, e := range g.OutgoingEdges(planID) {
		if g.Nodes[e.To].Type() == "wait.human" {
			gateID = e.To
		}
	}
	if gateID == "" {
		t.Fatal("plan should route to a human plan gate")
	}
	var implementID string
	backToPlan := false
	for _, e := range g.OutgoingEdges(gateID) {
		if e.To == planID {
			backToPlan = true
			continue
		}
		if g.Nodes[e.To].Type() == "codergen" {
			implementID = e.To
		}
	}
	if implementID == "" {
		t.Fatal("plan gate should approve into a codergen implement stage")
	}
	if !backToPlan {
		t.Error("plan gate should be able to route back to plan for a revise")
	}

	// Static checks: tool nodes running the repo's configured commands
	// ($context.check.*), each routing back to implement on failure.
	checkTools := 0
	for id, n := range g.Nodes {
		if n.Type() != "tool" || !strings.Contains(n.Attrs["tool_command"], "$context.check.") {
			continue
		}
		if baselineIDs[id] {
			continue // baseline copies terminate the run, they don't gate implement
		}
		checkTools++
		back := false
		for _, e := range g.OutgoingEdges(id) {
			if strings.Contains(e.Attrs["condition"], "outcome=fail") && e.To == implementID {
				back = true
			}
		}
		if !back {
			t.Errorf("check node %q does not route back to implement on failure", id)
		}
	}
	if checkTools < 4 {
		t.Fatalf("want >=4 static-check tool nodes (deps/typecheck/lint/test), got %d", checkTools)
	}

	// The self-review loop: a subgraph node inlining review-core (D6)
	// with jj diff.
	var loopID string
	for id, n := range g.Nodes {
		if n.Type() == "subgraph" {
			loopID = id
		}
	}
	if loopID == "" {
		t.Fatal("no subgraph self-review node")
	}
	loop := g.Nodes[loopID]
	if child := loop.Attrs["graph_ref"]; !strings.HasSuffix(child, "review-core/pipeline.dot") {
		t.Fatalf("review_loop graph_ref=%q, want suffix review-core/pipeline.dot", child)
	}
	if diffCmd := loop.Attrs["var.diff_cmd"]; !strings.Contains(diffCmd, "jj diff") {
		t.Fatalf("review_loop diff_cmd=%q, want it to contain `jj diff`", diffCmd)
	}

	// review_loop -> implement on FAIL, -> a human ship gate on PASS.
	var backToImplement bool
	var shipID string
	for _, e := range g.OutgoingEdges(loopID) {
		cond := e.Attrs["condition"]
		if strings.Contains(cond, "outcome=fail") && e.To == implementID {
			backToImplement = true
		}
		if strings.Contains(cond, "outcome=success") && g.Nodes[e.To].Type() == "wait.human" {
			shipID = e.To
		}
	}
	if !backToImplement {
		t.Error("review_loop should route back to implement on outcome=fail")
	}
	if shipID == "" {
		t.Fatal("review_loop should route to a human ship gate on outcome=success")
	}

	// The ship gate approves into a publish (draft-PR) codergen node,
	// which reaches exit.
	var publishID string
	for _, e := range g.OutgoingEdges(shipID) {
		if e.To != implementID && g.Nodes[e.To].Type() == "codergen" {
			publishID = e.To
		}
	}
	if publishID == "" {
		t.Fatal("ship gate should route to a publish node on approve")
	}
	toExit := false
	for _, e := range g.OutgoingEdges(publishID) {
		if g.Nodes[e.To].Type() == "exit" {
			toExit = true
		}
	}
	if !toExit {
		t.Error("publish node should route to exit")
	}
}

// TestImplementPipeline_FailRoutesBackThenPasses drives the review/fix
// loop end to end (RV4, post-D6): review-core is INLINED via a subgraph
// node, the first synth verdict FAILs and routes back to `implement`,
// the second passes and the run exits SUCCESS.
func TestImplementPipeline_FailRoutesBackThenPasses(t *testing.T) {
	childPath, err := filepath.Abs("../pipelines/review-core/pipeline.dot")
	must(t, err)
	src := fmt.Sprintf(`digraph implement {
		vars = "repo,identifier,url,title"
		max_node_visits = 3
		max_repeated_failures = 0
		start [shape=Mdiamond]
		implement [prompt="build the change"]
		review_loop [type="subgraph", graph_ref="%s",
		             var.diff_cmd="jj diff"]
		done [shape=Msquare]
		start -> implement
		implement -> review_loop
		review_loop -> implement [condition="outcome=fail"]
		review_loop -> done      [condition="outcome=success"]
	}`, childPath)
	prepared, err := setup.Prepare(setup.Options{Source: src})
	must(t, err)

	be := fake.New()
	be.SetText("implement", "implemented the change")
	for _, lens := range reviewCoreLenses {
		be.SetText("review_loop."+lens, "finding from "+lens)
	}
	// First review blocks (FAIL -> back to implement); second passes.
	be.SetSequence("review_loop.synth",
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

	out := runPrepared(t, prepared, be, map[string]string{
		"repo":       "owner/repo",
		"identifier": "ENG-42",
		"url":        "https://linear.app/eng-42",
		"title":      "Fix login",
	})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}

	// The FAIL verdict routed back to implement, which re-ran; the second
	// review's synth then passed and the run exited.
	if n := be.CallCount("implement"); n != 2 {
		t.Fatalf("implement called %d times, want 2 (fail routed back)", n)
	}
	if n := be.CallCount("review_loop.synth"); n != 2 {
		t.Fatalf("synth called %d times, want 2 (fail then pass)", n)
	}
	// The review fanned out to every inlined lens with the seeded diff_cmd.
	for _, lens := range reviewCoreLenses {
		if be.CallCount("review_loop."+lens) < 1 {
			t.Fatalf("lens %q never ran, want the self-review to fan out to it", lens)
		}
	}
	if got := childPrompt(t, be, "review_loop.correctness"); !strings.Contains(got, "jj diff") {
		t.Fatalf("lens prompt = %q, want it to contain `jj diff`", got)
	}
}
