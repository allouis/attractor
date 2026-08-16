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

// implementPipelineSrc reads the shipped plan-build-review pipeline (router-spec R6).
func implementPipelineSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../pipelines/plan-build-review/pipeline.dot")
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

	// start -> set_base (pins the review base with `jj new`) -> baseline_*
	// (tool chain proving that base is green) -> plan (codergen) ->
	// plan_gate (wait.human): a human approves the plan before any code is
	// written, and can send it back to plan.
	cursor := g.OutgoingEdges("start")[0].To
	if n := g.Nodes[cursor]; n.Type() != "tool" || !strings.Contains(n.Attrs["tool_command"], "jj new $context.base") {
		t.Fatalf("start should route to a set_base tool node pinning $context.base, got %q (%s)", cursor, g.Nodes[cursor].Type())
	}
	// set_base -> baseline (a checks-core subgraph) -> plan. baseline routes
	// to plan unconditionally, so a check failure (which has no outcome=fail
	// edge) terminates the run rather than routing to a fixer.
	cursor = g.OutgoingEdges(cursor)[0].To
	baselineID := cursor
	if b := g.Nodes[cursor]; b.Type() != "subgraph" || !strings.HasSuffix(b.Attrs["graph_ref"], "checks-core/pipeline.dot") {
		t.Fatalf("set_base should route to a checks-core baseline subgraph, got %q (%s)", cursor, g.Nodes[cursor].Type())
	}
	baselineEdges := g.OutgoingEdges(cursor)
	if len(baselineEdges) != 1 || strings.TrimSpace(baselineEdges[0].Attrs["condition"]) != "" {
		t.Fatalf("baseline should route to plan unconditionally (fail terminates), got %d edge(s)", len(baselineEdges))
	}
	planID := baselineEdges[0].To
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
	// The gate has two codergen branches: approve -> implement (routes
	// forward into the checks), revise -> revise_plan (a responder that
	// carries the human's note and loops back to the gate for re-approval).
	var implementID, reviseID string
	for _, e := range g.OutgoingEdges(gateID) {
		if g.Nodes[e.To].Type() != "codergen" {
			continue
		}
		loopsBackToGate := false
		for _, oe := range g.OutgoingEdges(e.To) {
			if oe.To == gateID {
				loopsBackToGate = true
			}
		}
		if loopsBackToGate {
			reviseID = e.To
		} else {
			implementID = e.To
		}
	}
	if implementID == "" {
		t.Fatal("plan gate should approve into a codergen implement stage")
	}
	if reviseID == "" {
		t.Error("plan gate should be able to route to a revise responder that re-gates")
	}

	// Post-implement gate: a SECOND checks-core subgraph (distinct from the
	// baseline). implement and both responders feed it; on success it routes
	// to the self-review, on failure to a codergen fix responder.
	var checksID string
	for id, n := range g.Nodes {
		if id != baselineID && n.Type() == "subgraph" && strings.HasSuffix(n.Attrs["graph_ref"], "checks-core/pipeline.dot") {
			checksID = id
		}
	}
	if checksID == "" {
		t.Fatal("want a post-implement checks-core gate subgraph distinct from baseline")
	}
	fedByImplement := false
	for _, e := range g.OutgoingEdges(implementID) {
		if e.To == checksID {
			fedByImplement = true
		}
	}
	if !fedByImplement {
		t.Error("implement should route into the checks gate")
	}
	var checksFail, checksOK string
	for _, e := range g.OutgoingEdges(checksID) {
		if strings.Contains(e.Attrs["condition"], "outcome=fail") {
			checksFail = e.To
		}
		if strings.Contains(e.Attrs["condition"], "outcome=success") {
			checksOK = e.To
		}
	}
	if checksFail == "" || g.Nodes[checksFail].Type() != "codergen" {
		t.Errorf("checks gate should route on failure to a codergen responder, got %q", checksFail)
	}
	if checksOK == "" {
		t.Error("checks gate should route to the self-review on success")
	}

	// The self-review loop: a subgraph node inlining review-core (D6)
	// with jj diff.
	var loopID string
	for id, n := range g.Nodes {
		if n.Type() == "subgraph" && strings.HasSuffix(n.Attrs["graph_ref"], "review-core/pipeline.dot") {
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

	// review_loop -> a review responder (respond_to_review) on FAIL,
	// -> a human ship gate on PASS.
	var reviewResponder string
	var shipID string
	for _, e := range g.OutgoingEdges(loopID) {
		cond := e.Attrs["condition"]
		if strings.Contains(cond, "outcome=fail") {
			reviewResponder = e.To
		}
		if strings.Contains(cond, "outcome=success") && g.Nodes[e.To].Type() == "wait.human" {
			shipID = e.To
		}
	}
	if reviewResponder == "" || reviewResponder == implementID || g.Nodes[reviewResponder].Type() != "codergen" {
		t.Errorf("review_loop should route on failure to a codergen responder, got %q", reviewResponder)
	}
	if shipID == "" {
		t.Fatal("review_loop should route to a human ship gate on outcome=success")
	}

	// The ship gate approves into a publish (draft-PR) codergen node that
	// reaches exit; its other branch requests changes via the responder.
	var publishID string
	for _, e := range g.OutgoingEdges(shipID) {
		if g.Nodes[e.To].Type() != "codergen" {
			continue
		}
		for _, oe := range g.OutgoingEdges(e.To) {
			if g.Nodes[oe.To].Type() == "exit" {
				publishID = e.To
			}
		}
	}
	if publishID == "" {
		t.Fatal("ship gate should route to a publish node that reaches exit on approve")
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
