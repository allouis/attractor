package attractor_test

import (
	"os"
	"strings"
	"testing"

	graphpkg "github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/lint"
)

// revisePRPipelineSrc reads the shipped revise-pr pipeline.
func revisePRPipelineSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../pipelines/revise-pr/pipeline.dot")
	must(t, err)
	return string(b)
}

func buildRevisePRGraph(t *testing.T) *graphpkg.Graph {
	t.Helper()
	return build(t, revisePRPipelineSrc(t))
}

// TestRevisePRPipeline_LintsClean confirms the shipped revise-pr pipeline is
// structurally valid: it parses, builds, and lints with no ERROR (the
// acp_command_missing warning class is known-ok).
func TestRevisePRPipeline_LintsClean(t *testing.T) {
	g := buildRevisePRGraph(t)
	diags, err := lint.ValidateOrError(g)
	if err != nil {
		t.Fatalf("revise-pr pipeline rejected: %v", err)
	}
	for _, d := range diags {
		if d.Severity == lint.Error {
			t.Fatalf("unexpected ERROR: %+v", d)
		}
	}
}

// TestRevisePRPipeline_RequiresWorkspaceRevision pins that workspace_revision
// is a REQUIRED var: without it seeded an external launcher would review the host's
// @ instead of the PR branch, so a dispatch that forgets it must be rejected.
func TestRevisePRPipeline_RequiresWorkspaceRevision(t *testing.T) {
	g := buildRevisePRGraph(t)
	want := map[string]bool{"repo": true, "pr_number": true, "bookmark": true, "workspace_revision": true}
	for _, v := range g.DeclaredVars() {
		delete(want, v)
	}
	if len(want) != 0 {
		t.Fatalf("revise-pr must declare vars including workspace_revision; missing %v", want)
	}
}

// TestRevisePRPipeline_Structure pins the loop shape: a baseline check chain
// runs first and terminates the run on a red branch (no fail edge); a
// review_loop over review-core with a LOCAL jj diff routes to a human ship
// gate on PASS and to `fix` on FAIL; `fix` re-runs the deterministic checks
// (each routing back to fix) and loops into review; and the push node is
// reached ONLY from the ship gate's push branch, then exits.
func TestRevisePRPipeline_Structure(t *testing.T) {
	g := buildRevisePRGraph(t)

	// start -> baseline (a checks-core subgraph) -> review_loop. baseline
	// routes onward unconditionally, so a red baseline (no outcome=fail
	// edge) terminates the run instead of routing to a fixer.
	cursor := g.OutgoingEdges("start")[0].To
	baselineID := cursor
	if b := g.Nodes[cursor]; b.Type() != "subgraph" || !strings.HasSuffix(b.Attrs["graph_ref"], "checks-core/pipeline.dot") {
		t.Fatalf("start should route to a checks-core baseline subgraph, got %q (%s)", cursor, g.Nodes[cursor].Type())
	}
	bEdges := g.OutgoingEdges(cursor)
	if len(bEdges) != 1 || strings.TrimSpace(bEdges[0].Attrs["condition"]) != "" {
		t.Fatalf("baseline must route onward unconditionally (a red baseline terminates), got %d edge(s)", len(bEdges))
	}

	// The node the baseline lands on is the review loop.
	loopID := bEdges[0].To
	loop := g.Nodes[loopID]
	if loop.Type() != "subgraph" {
		t.Fatalf("baseline should land on the review subgraph (D6), got %q (type %q)", loopID, loop.Type())
	}
	if child := loop.Attrs["graph_ref"]; !strings.HasSuffix(child, "review-core/pipeline.dot") {
		t.Fatalf("review_loop graph_ref=%q, want suffix review-core/pipeline.dot", child)
	}
	diffCmd := loop.Attrs["var.diff_cmd"]
	if !strings.Contains(diffCmd, "jj diff") {
		t.Fatalf("review_loop diff_cmd=%q, want a local `jj diff` (unpushed work must be visible)", diffCmd)
	}
	if !strings.Contains(diffCmd, "trunk()") || strings.Contains(diffCmd, "gh pr diff") {
		t.Fatalf("review_loop diff_cmd=%q, want the local trunk()..@ diff, not `gh pr diff`", diffCmd)
	}

	// review_loop -> a human ship gate on PASS, -> fix (codergen) on FAIL.
	var shipID, fixID string
	for _, e := range g.OutgoingEdges(loopID) {
		cond := e.Attrs["condition"]
		if strings.Contains(cond, "outcome=success") && g.Nodes[e.To].Type() == "wait.human" {
			shipID = e.To
		}
		if strings.Contains(cond, "outcome=fail") && g.Nodes[e.To].Type() == "codergen" {
			fixID = e.To
		}
	}
	if shipID == "" {
		t.Fatal("review_loop should route to a human ship gate on outcome=success")
	}
	if fixID == "" {
		t.Fatal("review_loop should route to a codergen fix node on outcome=fail")
	}

	// fix -> checks (a SECOND checks-core subgraph, distinct from baseline):
	// a failure routes back to fix, success re-enters the review loop,
	// closing the fix -> checks -> review cycle.
	var checksID string
	for _, e := range g.OutgoingEdges(fixID) {
		if to := g.Nodes[e.To]; to.Type() == "subgraph" && strings.HasSuffix(to.Attrs["graph_ref"], "checks-core/pipeline.dot") {
			checksID = e.To
		}
	}
	if checksID == "" || checksID == baselineID {
		t.Fatal("fix should route into a post-fix checks-core gate distinct from baseline")
	}
	var backToFixOnCheck, reenters bool
	for _, e := range g.OutgoingEdges(checksID) {
		if strings.Contains(e.Attrs["condition"], "outcome=fail") && e.To == fixID {
			backToFixOnCheck = true
		}
		if strings.Contains(e.Attrs["condition"], "outcome=success") && e.To == loopID {
			reenters = true
		}
	}
	if !backToFixOnCheck {
		t.Error("checks gate should route back to fix on failure")
	}
	if !reenters {
		t.Error("checks gate should re-enter review_loop on success; the fix/review cycle is broken")
	}

	// The ship gate: a [P] push branch to the publish node and a [C] change
	// branch back to fix, both human-labelled.
	var pushID string
	backToFix := false
	for _, e := range g.OutgoingEdges(shipID) {
		if e.Attrs["label"] == "" {
			t.Errorf("ship-gate edge to %q has no human label", e.To)
		}
		switch {
		case g.Nodes[e.To].Type() == "codergen" && e.To != fixID:
			pushID = e.To
		case e.To == fixID:
			backToFix = true
		}
	}
	if pushID == "" {
		t.Fatal("ship gate should route to a publish (push) codergen node on approve")
	}
	if !backToFix {
		t.Error("ship gate should be able to route back to fix (request changes)")
	}

	// The push node is reached ONLY via the ship gate — never from the
	// review/fix cycle, so nothing publishes without human approval.
	for _, e := range g.IncomingEdges(pushID) {
		if e.From != shipID {
			t.Fatalf("push node %q has an incoming edge from %q; it must be reachable only via the ship gate", pushID, e.From)
		}
	}
	// ...and it exits.
	toExit := false
	for _, e := range g.OutgoingEdges(pushID) {
		if g.Nodes[e.To].Type() == "exit" {
			toExit = true
		}
	}
	if !toExit {
		t.Error("push node should route to exit")
	}
}
