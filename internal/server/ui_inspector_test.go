package server

import (
	"strings"
	"testing"
)

// The R3 node inspector is built from three server-computed sources — /state
// (status, attempts, timing), /nodes (the resolved prompt), and /stages (the
// response, or a tool's output + exit code). These tests drive the pure
// builder nodeInspectorHtml directly; selectNode wires the fetches.

// A completed codergen node shows its state (status + timing from /state), its
// resolved prompt, and its response rendered as markdown.
func TestNodeInspectorCompletedCodergen(t *testing.T) {
	html := evalUI(t, `nodeInspectorHtml({
	  id:'plan',
	  state:{status:'completed',attempts:1,started_at:'2026-08-02T10:00:00Z',completed_at:'2026-08-02T10:00:02Z',outcome:'success'},
	  node:{type:'codergen',prompt:'write the plan'},
	  stage:{response:'# Plan\n- do **X**'}
	})`)
	for _, want := range []string{"plan", "completed", "2.0s", "write the plan",
		`<h1 class="md-h">Plan</h1>`, "<strong>X</strong>", "Response"} {
		if !strings.Contains(html, want) {
			t.Errorf("completed codergen inspector missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "unavailable") {
		t.Errorf("completed node must never read as unavailable:\n%s", html)
	}
}

// A tool node shows its exit code and its captured stdout/stderr, ANSI-rendered
// (SGR colours become theme spans; no raw escape reaches the operator).
func TestNodeInspectorToolNode(t *testing.T) {
	html := evalUI(t, `nodeInspectorHtml({
	  id:'test',
	  state:{status:'failed',attempts:1,started_at:'2026-08-02T10:00:00Z',completed_at:'2026-08-02T10:12:20Z',outcome:'fail'},
	  node:{type:'tool',prompt:''},
	  stage:{response:''},
	  output:'\u001b[31mFAIL\u001b[0m suite',
	  exit:2
	})`)
	for _, want := range []string{"test", "exit 2", "Output", "12m 20s", "ansi-red", "FAIL"} {
		if !strings.Contains(html, want) {
			t.Errorf("tool inspector missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "\x1b[") {
		t.Errorf("raw ANSI escape leaked into the tool inspector:\n%s", html)
	}
	if strings.Contains(html, "unavailable") {
		t.Errorf("completed tool node must never read as unavailable:\n%s", html)
	}
}

// A pending (yet-to-run) node is informative, not broken: it shows the resolved
// prompt with a "not started" framing — never "stage detail unavailable".
func TestNodeInspectorPending(t *testing.T) {
	html := evalUI(t, `nodeInspectorHtml({
	  id:'open_pr',
	  state:{status:'pending'},
	  node:{type:'tool',prompt:'open the pull request'},
	  stage:null
	})`)
	for _, want := range []string{"open_pr", "not started", "open the pull request", "pending"} {
		if !strings.Contains(html, want) {
			t.Errorf("pending inspector missing %q:\n%s", want, html)
		}
	}
	for _, bad := range []string{"unavailable", "Output", "Response"} {
		if strings.Contains(html, bad) {
			t.Errorf("pending node should not render %q (nothing ran yet):\n%s", bad, html)
		}
	}
}

// A running node points at the live transcript in the feed rather than an
// empty response — and never reads as unavailable.
func TestNodeInspectorRunning(t *testing.T) {
	html := evalUI(t, `nodeInspectorHtml({
	  id:'implement',
	  state:{status:'running',attempts:1,started_at:'2026-08-02T10:00:00Z'},
	  node:{type:'codergen',prompt:'do it'},
	  stage:null
	})`)
	for _, want := range []string{"running", "do it", "feed"} {
		if !strings.Contains(html, want) {
			t.Errorf("running inspector missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "unavailable") {
		t.Errorf("running node must not read as unavailable:\n%s", html)
	}
}

// A multi-attempt node names its attempt count (the per-attempt breakdown lives
// in the scoped feed slice below).
func TestNodeInspectorMultiAttempt(t *testing.T) {
	html := evalUI(t, `nodeInspectorHtml({
	  id:'flaky',
	  state:{status:'completed',attempts:2,started_at:'2026-08-02T10:00:00Z',completed_at:'2026-08-02T10:00:03Z',outcome:'success'},
	  node:{type:'tool'},
	  stage:{},
	  output:'recovered',
	  exit:0
	})`)
	if !strings.Contains(html, "2 attempts") {
		t.Errorf("multi-attempt node should name its attempts:\n%s", html)
	}
	// A single-attempt node does not clutter the header with "1 attempts".
	one := evalUI(t, `nodeInspectorHtml({id:'plan',state:{status:'completed',attempts:1},node:{},stage:null})`)
	if strings.Contains(one, "attempts") {
		t.Errorf("single-attempt node should not show an attempts count:\n%s", one)
	}
}

// Every inspector field is attacker-controlled (node ids from the DOT source,
// prompt/response/output are agent- or tool-produced) and must render inert.
func TestNodeInspectorEscapes(t *testing.T) {
	x := "<img src=x onerror=alert(1)>"
	tool := evalUI(t, `nodeInspectorHtml({id:'`+x+`',state:{status:'`+x+`'},node:{type:'tool',prompt:'`+x+`'},stage:{},output:'`+x+`',exit:0})`)
	coder := evalUI(t, `nodeInspectorHtml({id:'n',state:{status:'completed'},node:{type:'codergen',prompt:'`+x+`'},stage:{response:'`+x+`'}})`)
	for _, html := range []string{tool, coder} {
		if strings.Contains(html, "<img src=x") {
			t.Errorf("inspector leaked attacker markup:\n%s", html)
		}
	}
}

// The inspector still surfaces a node's forwarded child-pipeline findings
// (T9b) inline, in the #node-findings container recordNodeFinding refreshes.
func TestNodeInspectorRendersFindings(t *testing.T) {
	html := evalUI(t, `nodeInspectorHtml({id:'review_loop',state:{status:'completed'},node:{},stage:null,findings:['finding X']})`)
	for _, want := range []string{"finding X", "Findings", "resolved", `id="node-findings"`} {
		if !strings.Contains(html, want) {
			t.Errorf("inspector missing findings %q:\n%s", want, html)
		}
	}
	empty := evalUI(t, `nodeInspectorHtml({id:'plan',state:{status:'running'},node:{},stage:null})`)
	if strings.Contains(empty, "Findings") {
		t.Errorf("a node with no findings should show no findings section:\n%s", empty)
	}
}
