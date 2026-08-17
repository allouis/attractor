package attractor_test

import (
	"os"
	"strings"
	"testing"
)

// TestAmendPRPipeline_Structure pins what makes amend-pr differ from
// plan-build-review: it runs the same plan→build→review cycle on an
// EXISTING PR, so there is NO fresh-base setup, the review covers the whole
// PR (trunk()..@), and it ends by pushing the branch rather than opening a
// new PR.
func TestAmendPRPipeline_Structure(t *testing.T) {
	b, err := os.ReadFile("../pipelines/amend-pr/pipeline.dot")
	must(t, err)
	g := build(t, string(b)) // raw graph — subgraphs not inlined

	// No fresh base: nothing runs `jj new` (that would discard the PR), and
	// start routes straight to a codergen plan, not a set_base tool node.
	for id, n := range g.Nodes {
		if strings.Contains(n.Attrs["tool_command"], "jj new") {
			t.Fatalf("amend-pr must not create a fresh base, but %q runs `jj new`", id)
		}
	}
	planID := g.OutgoingEdges("start")[0].To
	if g.Nodes[planID].Type() != "codergen" {
		t.Fatalf("start should route straight to a codergen plan (no set_base), got %q (%s)", planID, g.Nodes[planID].Type())
	}

	// plan -> plan_gate (wait.human) -> implement (codergen) + revise_plan.
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
	for _, e := range g.OutgoingEdges(gateID) {
		if g.Nodes[e.To].Type() == "codergen" {
			backToGate := false
			for _, oe := range g.OutgoingEdges(e.To) {
				if oe.To == gateID {
					backToGate = true
				}
			}
			if !backToGate {
				implementID = e.To
			}
		}
	}
	if implementID == "" {
		t.Fatal("plan gate should approve into a codergen implement stage")
	}

	// Two subgraphs: a checks-core gate and a review-core loop over the
	// whole PR (trunk()..@).
	var checksID, reviewID string
	for id, n := range g.Nodes {
		if n.Type() != "subgraph" {
			continue
		}
		switch {
		case strings.HasSuffix(n.Attrs["graph_ref"], "checks-core/pipeline.dot"):
			checksID = id
		case strings.HasSuffix(n.Attrs["graph_ref"], "review-core/pipeline.dot"):
			reviewID = id
			if d := n.Attrs["var.diff_cmd"]; !strings.Contains(d, "trunk()") {
				t.Errorf("review diff_cmd=%q, want the whole-PR trunk()..@ diff", d)
			}
		}
	}
	if checksID == "" || reviewID == "" {
		t.Fatalf("want a checks-core gate and a review-core loop, got checks=%q review=%q", checksID, reviewID)
	}

	// checks fail -> a codergen fixer.
	failTarget := func(from string) string {
		for _, e := range g.OutgoingEdges(from) {
			if strings.Contains(e.Attrs["condition"], "outcome=fail") {
				return e.To
			}
		}
		return ""
	}
	if to := failTarget(checksID); to == "" || g.Nodes[to].Type() != "codergen" {
		t.Errorf("checks gate should route on failure to a codergen fixer, got %q", to)
	}
	// review fail -> a codergen responder.
	if to := failTarget(reviewID); to == "" || g.Nodes[to].Type() != "codergen" {
		t.Errorf("review loop should route on failure to a codergen responder, got %q", to)
	}
	var shipID string
	for _, e := range g.OutgoingEdges(reviewID) {
		if strings.Contains(e.Attrs["condition"], "outcome=success") && g.Nodes[e.To].Type() == "wait.human" {
			shipID = e.To
		}
	}
	if shipID == "" {
		t.Fatal("review loop should route to a human ship gate on success")
	}

	// Ship approves into a publish codergen that reaches exit — and that
	// publish reuses the push prompt (no open-pr / no PR creation).
	var pushID string
	for _, e := range g.OutgoingEdges(shipID) {
		if g.Nodes[e.To].Type() != "codergen" {
			continue
		}
		for _, oe := range g.OutgoingEdges(e.To) {
			if g.Nodes[oe.To].Type() == "exit" {
				pushID = e.To
			}
		}
	}
	if pushID == "" {
		t.Fatal("ship gate should route to a publish node that reaches exit")
	}
	if p := g.Nodes[pushID].Attrs["prompt"]; !strings.Contains(p, "push-updates") {
		t.Errorf("publish node should reuse the push-updates prompt (update PR in place), got %q", p)
	}
	if strings.Contains(g.Nodes[pushID].Attrs["prompt"], "open-pr") {
		t.Error("amend-pr must not open a new PR")
	}
}
