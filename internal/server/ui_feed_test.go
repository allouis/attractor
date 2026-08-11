package server

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ---- pure feed builders (ui-run-view-v3 R2) ----------------------------

// TestFeedDivider: a lifecycle divider is node chip · attempt · duration, with
// the duration formatted like the mock (nanoseconds → "7m 07s").
func TestFeedDivider(t *testing.T) {
	html := evalUI(t, `feDividerHtml({node:"plan",attempt:1,durNs:427000000000,status:"completed"})`)
	for _, want := range []string{`data-kind="lifecycle"`, `data-node="plan"`, "attempt 1", "7m 07s"} {
		if !strings.Contains(html, want) {
			t.Errorf("divider missing %q:\n%s", want, html)
		}
	}
	// A still-running stage shows "running", not a duration.
	if r := evalUI(t, `feDividerHtml({node:"plan",attempt:2})`); !strings.Contains(r, "running") {
		t.Errorf("running divider wrong:\n%s", r)
	}
	// A finished stage that carried no duration (stage_failed has none) still
	// reads as done, never as a stuck "running".
	if r := evalUI(t, `feDividerHtml({node:"test",attempt:1,status:"failed"})`); strings.Contains(r, "running") {
		t.Errorf("failed divider must not read as running:\n%s", r)
	}
}

// TestFeedFailedDividerDuration: stage_failed carries no duration_ns, so the
// ingest fills the divider's duration from the start→end timestamp span.
func TestFeedFailedDividerDuration(t *testing.T) {
	out := runFeedHarness(t, `
fire('stage_started', { node_id: 'test', attempt: 1, ts: '2026-08-11T12:00:00Z' });
fire('stage_failed',  { node_id: 'test', status: 'fail', message: 'tool: exit status 1', ts: '2026-08-11T12:12:20Z' });
process.stdout.write(JSON.stringify({
  dur: read('(feed.find(e => e.t==="divider" && e.node==="test")||{}).durNs'),
  status: read('(feed.find(e => e.t==="divider" && e.node==="test")||{}).status'),
}));
`)
	// 12m20s = 740s = 740e9 ns.
	if !strings.Contains(out, `"dur":740000000000`) || !strings.Contains(out, `"status":"failed"`) {
		t.Errorf("failed divider duration not computed from timestamps:\n%s", out)
	}
}

// TestFmtDurNs pins the feed's duration formatting across the three ranges.
func TestFmtDurNs(t *testing.T) {
	cases := []struct {
		expr, want string
	}{
		{`fmtDurNs(2999851558)`, "3.0s"},
		{`fmtDurNs(427000000000)`, "7m 07s"},
		{`fmtDurNs(740000000000)`, "12m 20s"},
		{`fmtDurNs(6420000000000)`, "1h 47m"},
		{`fmtDurNs(null)`, ""},
	}
	for _, c := range cases {
		if got := evalUI(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestFeedProse: a prose bubble renders markdown, a collapsed per-group tool
// count, and the live dot while streaming.
func TestFeedProse(t *testing.T) {
	html := evalUI(t, `feProseHtml({node:"implement",attempt:2,text:"Added **X** and `+"`code`"+`",tools:["[bash] ls","[read] f"],live:true})`)
	for _, want := range []string{
		`data-kind="prose"`, "<strong>X</strong>", `md-inline-code`,
		"<summary>2 tool calls</summary>", "fe-live", "[bash] ls",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("prose missing %q:\n%s", want, html)
		}
	}
	// A finished bubble carries no live dot and, with one tool, singular label.
	one := evalUI(t, `feProseHtml({node:"plan",attempt:1,text:"hi",tools:["x"],live:false})`)
	if strings.Contains(one, "fe-live") || !strings.Contains(one, "1 tool call<") {
		t.Errorf("single/finished bubble wrong:\n%s", one)
	}
}

// TestFeedCheckFailed: a failed check shows its tail (not the head), a red
// outcome, the command + exit code, and a "show full output" control because
// the output was clamped.
func TestFeedCheckFailed(t *testing.T) {
	// 60 lines → the 40-line tail drops the head; line 1 is gone, line 60 stays.
	html := evalUI(t, `feCheckHtml({node:"test",attempt:1,status:"failed",durNs:740000000000,cmd:"pnpm test",exit:1,output:Array.from({length:60},(_,i)=>"line"+(i+1)).join("\n"),expanded:false})`)
	for _, want := range []string{"fe-check fail", "pnpm test", "exit 1", "12m 20s", "line60", "show full output"} {
		if !strings.Contains(html, want) {
			t.Errorf("failed check missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, ">line1<") || strings.Contains(html, "line1\n") {
		t.Errorf("failed check tail should have dropped the head:\n%s", html)
	}
}

// TestFeedCheckSuccess: a passing check is exit 0 (green) with output not yet
// fetched — a bare "▸ output" toggle, no auto-expanded body.
func TestFeedCheckSuccess(t *testing.T) {
	html := evalUI(t, `feCheckHtml({node:"deps",attempt:1,status:"completed",durNs:2999851558,cmd:"pnpm i",exit:0,output:null,expanded:false})`)
	for _, want := range []string{"exit 0", "3.0s", "pnpm i", "▸ output"} {
		if !strings.Contains(html, want) {
			t.Errorf("success check missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "fe-check fail") {
		t.Errorf("passing check must not read as failed:\n%s", html)
	}
}

// TestFeedCheckHeadTail: expanding a huge output keeps head AND tail with the
// middle elided (collapse-never-truncate), and offers to collapse.
func TestFeedCheckHeadTail(t *testing.T) {
	html := evalUI(t, `feCheckHtml({node:"test",attempt:1,status:"failed",cmd:"x",exit:1,output:Array.from({length:1000},(_,i)=>"L"+(i+1)).join("\n"),expanded:true})`)
	for _, want := range []string{"L1", "L1000", "lines elided", "collapse output"} {
		if !strings.Contains(html, want) {
			t.Errorf("head+tail expand missing %q:\n%s", want, html)
		}
	}
}

// TestTailAndHeadTail exercises the clamp helpers directly.
func TestTailAndHeadTail(t *testing.T) {
	if got := evalUI(t, `JSON.stringify(tailLines("a\nb\nc\nd", 2))`); !strings.Contains(got, `"c\nd"`) || !strings.Contains(got, `"clamped":true`) {
		t.Errorf("tailLines wrong: %s", got)
	}
	if got := evalUI(t, `JSON.stringify(tailLines("a\nb", 5))`); !strings.Contains(got, `"clamped":false`) {
		t.Errorf("short tailLines should not clamp: %s", got)
	}
	if got := evalUI(t, `headTail("1\n2\n3\n4\n5\n6", 1).text`); !strings.Contains(got, "1") || !strings.Contains(got, "6") || !strings.Contains(got, "elided") {
		t.Errorf("headTail wrong: %s", got)
	}
}

// TestFeedGateTurn: an answered gate renders the question, the reviewed node's
// plan snippet as markdown, and the answer bubble (chosen label + note). An
// unanswered gate shows "waiting for you" and no answer bubble.
func TestFeedGateTurn(t *testing.T) {
	answered := evalUI(t, `feGateHtml({node:"plan_gate",qid:"q",question:{text:"Does the plan make sense?"},precedingNode:"plan",planText:"# Plan\n- item",answered:true,answeredAfterNs:600000000000,answer:{label:"[A] Approve plan",note:"keep the base class"}})`)
	for _, want := range []string{
		"Does the plan make sense?", "answered after 10m 00s",
		"Show plan output", `<h1 class="md-h">Plan</h1>`,
		"fe-answer", "[A] Approve plan", "keep the base class",
	} {
		if !strings.Contains(answered, want) {
			t.Errorf("answered gate missing %q:\n%s", want, answered)
		}
	}
	pending := evalUI(t, `feGateHtml({node:"plan_gate",qid:"q",question:{text:"OK?"},precedingNode:"plan",planText:"",answered:false})`)
	if !strings.Contains(pending, "waiting for you") || strings.Contains(pending, "fe-answer") {
		t.Errorf("pending gate wrong:\n%s", pending)
	}
}

// TestFeedCrumb: the breadcrumb names the scope (whole run vs a node with a
// "show all" reset) and reflects each active kind filter.
func TestFeedCrumb(t *testing.T) {
	whole := evalUI(t, `(feedScope="", feedKinds.prose=true, feedKinds.tools=false, feedCrumbHtml())`)
	if !strings.Contains(whole, "whole run") {
		t.Errorf("whole-run crumb wrong:\n%s", whole)
	}
	if !strings.Contains(whole, `data-feed-kind="prose"`) || !strings.Contains(whole, `fe-filter active" data-feed-kind="prose"`) {
		t.Errorf("active prose chip missing:\n%s", whole)
	}
	if strings.Contains(whole, `fe-filter active" data-feed-kind="tools"`) {
		t.Errorf("tools chip should be inactive:\n%s", whole)
	}
	scoped := evalUI(t, `(feedScope="implement", feedCrumbHtml())`)
	if !strings.Contains(scoped, "<b>implement</b>") || !strings.Contains(scoped, `data-feed-scope=""`) {
		t.Errorf("scoped crumb missing node + show-all:\n%s", scoped)
	}
}

// TestFeedEntryEscapesXSS: every feed field is attacker-controlled (node ids
// from the DOT source, agent prose, tool commands, question text) and must not
// render as live markup.
func TestFeedEntryEscapesXSS(t *testing.T) {
	x := "<img src=x onerror=alert(1)>"
	for _, expr := range []string{
		`feDividerHtml({node:"` + x + `",attempt:1})`,
		`feProseHtml({node:"n",attempt:1,text:"` + x + `",tools:["` + x + `"]})`,
		`feCheckHtml({node:"n",attempt:1,status:"failed",cmd:"` + x + `",exit:1,output:"` + x + `",expanded:false})`,
		`feGateHtml({node:"n",question:{text:"` + x + `"},answered:false})`,
	} {
		if html := evalUI(t, expr); strings.Contains(html, "<img src=x") {
			t.Errorf("feed entry leaked attacker markup:\n%s\nfrom %s", html, expr)
		}
	}
}

// ---- feed ingest / classification (stream harness) ---------------------

// runFeedHarness drives the real attachStream + feedIngest from index.html
// through a fake EventSource and a minimal DOM, fires a sequence of events, and
// lets `script` read the feed model via read('...'). It mirrors
// runStreamHarness but supplies setTimeout (the feed's debounced render) and a
// forgiving document so a scheduled render never throws.
func runFeedHarness(t *testing.T, script string) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI feed test")
	}
	uiPath, err := filepath.Abs("ui/index.html")
	if err != nil {
		t.Fatal(err)
	}
	harness := `
const fs = require('fs');
const vm = require('vm');
const html = fs.readFileSync(process.argv[1], 'utf8');
const m = html.match(/<script>([\s\S]*?)<\/script>/);
if (!m) { console.error('no <script> found'); process.exit(2); }
const listeners = {};
let onopenCb = null;
class FakeES {
  constructor(url) { this.url = url; }
  addEventListener(kind, fn) { (listeners[kind] = listeners[kind] || []).push(fn); }
  set onopen(fn) { onopenCb = fn; }
  set onerror(fn) {}
  close() {}
}
const els = {};
const mkEl = () => ({ innerHTML: '', textContent: '', hidden: false, scrollTop: 0, scrollHeight: 0,
  classList: { toggle() {}, contains() { return false; }, add() {}, remove() {} },
  querySelectorAll: () => [], querySelector: () => null, dataset: {},
  getAttribute: () => null, setAttribute() {}, addEventListener() {}, insertAdjacentHTML() {} });
const sandbox = {
  window: { addEventListener() {}, innerHeight: 800, scrollY: 0, scrollTo() {} },
  document: { getElementById: id => (els[id] = els[id] || mkEl()), body: { scrollHeight: 1000 } },
  location: {}, console, EventSource: FakeES, setTimeout, clearTimeout,
  requestAnimationFrame: () => {},
  fetch: () => Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}), text: () => Promise.resolve('') }),
};
vm.createContext(sandbox);
vm.runInContext(m[1], sandbox);
const fire = (kind, ev) => (listeners[kind] || []).forEach(fn => fn({ data: JSON.stringify(ev) }));
const read = expr => vm.runInContext(expr, sandbox);
sandbox.attachStream('run1');
if (onopenCb) onopenCb();
` + script
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	return string(out)
}

// feedSequence is a realistic slice of a run: a codergen node with prose + a
// tool call, a human gate answered, then two check nodes (one passing, one
// failing). Shared by the classification and idempotency tests.
const feedSequence = `
fire('stage_started',   { node_id: 'start', attempt: 1, ts: 't0' });
fire('stage_completed', { node_id: 'start', status: 'success', duration_ns: 400, ts: 't0' });
fire('stage_started',   { node_id: 'plan', attempt: 1, ts: 't1' });
fire('stage_progress',  { node_id: 'plan', message: 'Planning **it**.', detail: { kind: 'assistant_delta' }, ts: 't1' });
fire('stage_progress',  { node_id: 'plan', message: 'ls -la', detail: { kind: 'tool_call' }, ts: 't1' });
fire('stage_completed', { node_id: 'plan', status: 'success', duration_ns: 427000000000, ts: 't2' });
fire('interview_started',  { node_id: 'plan_gate', question_id: 'g1', message: 'OK?', question: { text: 'OK?', options: [] }, ts: 't2' });
fire('interview_answered', { node_id: 'plan_gate', question_id: 'g1', ts: 't3' });
fire('stage_started',   { node_id: 'implement', attempt: 1, ts: 't3' });
fire('stage_progress',  { node_id: 'implement', message: 'Doing it.', detail: { kind: 'assistant_delta' }, ts: 't3' });
fire('stage_completed', { node_id: 'implement', status: 'success', duration_ns: 60000000000, ts: 't4' });
fire('stage_started',   { node_id: 'deps', attempt: 1, ts: 't4' });
fire('stage_completed', { node_id: 'deps', status: 'success', duration_ns: 3000000000, ts: 't4' });
fire('stage_started',   { node_id: 'test', attempt: 1, ts: 't5' });
fire('stage_failed',    { node_id: 'test', status: 'fail', message: 'tool: exit status 1: $ pnpm test', duration_ns: 740000000000, ts: 't6' });
`

// TestFeedIngestClassifies: the ingest turns the event stream into typed feed
// entries — dividers for lifecycle, a prose bubble for the codergen node
// (carrying its markdown + tool call), a first-class gate turn, and check
// blocks for the tool nodes (never a divider/check for structural `start`).
func TestFeedIngestClassifies(t *testing.T) {
	out := runFeedHarness(t, feedSequence+`
process.stdout.write(JSON.stringify({
  types: read('feed.map(e => e.t)'),
  nodes: read('feed.map(e => e.node)'),
  planDur: read('(feed.find(e => e.t==="divider" && e.node==="plan")||{}).durNs'),
  proseText: read('(feed.find(e => e.t==="prose" && e.node==="plan")||{}).text'),
  proseTools: read('(feed.find(e => e.t==="prose" && e.node==="plan")||{}).tools'),
  testStatus: read('(feed.find(e => e.t==="check" && e.node==="test")||{}).status'),
  testExit: read('(feed.find(e => e.t==="check" && e.node==="test")||{}).exit'),
  depsIsCheck: read('!!feed.find(e => e.t==="check" && e.node==="deps")'),
  implementIsProse: read('!!feed.find(e => e.t==="prose" && e.node==="implement")'),
  startEntries: read('feed.filter(e => e.node==="start").length'),
}));
`)
	for _, want := range []string{
		`"testStatus":"failed"`, `"testExit":1`,
		`"depsIsCheck":true`, `"implementIsProse":true`,
		`"startEntries":0`, `"planDur":427000000000`,
		"Planning **it**.", `"ls -la"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("feed classification missing %q:\n%s", want, out)
		}
	}
	// The plan node is a prose node, not a check (it produced a bubble).
	if strings.Contains(out, `{"t":"check","node":"plan"`) {
		t.Errorf("a node that produced prose was misclassified as a check:\n%s", out)
	}
}

// TestFeedGateAnswered: a gate becomes a first-class turn that flips to
// answered once interview_answered arrives, and its plumbing divider is
// suppressed (the gate box replaces it).
func TestFeedGateAnswered(t *testing.T) {
	out := runFeedHarness(t, feedSequence+`
process.stdout.write(JSON.stringify({
  gates: read('feed.filter(e => e.t==="gate").length'),
  answered: read('(feed.find(e => e.t==="gate")||{}).answered'),
  preceding: read('(feed.find(e => e.t==="gate")||{}).precedingNode'),
  gateDividerSuppressed: read('(feed.find(e => e.t==="divider" && e.node==="plan_gate")||{suppressed:"none"}).suppressed'),
}));
`)
	for _, want := range []string{`"gates":1`, `"answered":true`, `"preceding":"plan"`} {
		if !strings.Contains(out, want) {
			t.Errorf("gate turn wrong, missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"gateDividerSuppressed":false`) || strings.Contains(out, `"gateDividerSuppressed":"none"`) {
		// Either the divider is marked suppressed, or none was kept — both are
		// fine, but an unsuppressed gate divider would double the gate row.
		if strings.Contains(out, `"gateDividerSuppressed":false`) {
			t.Errorf("gate's plumbing divider not suppressed:\n%s", out)
		}
	}
}

// TestFeedGateAnswerFromEventDetail: a replayed/reloaded run has no live
// answer capture, so the gate's answer bubble is filled from the answer content
// the daemon now records on the interview_answered event (chosen label + note).
func TestFeedGateAnswerFromEventDetail(t *testing.T) {
	out := runFeedHarness(t, `
fire('stage_started',   { node_id: 'plan', attempt: 1, ts: 't0' });
fire('stage_completed', { node_id: 'plan', status: 'success', duration_ns: 400, ts: 't1' });
fire('interview_started',  { node_id: 'plan_gate', question_id: 'g1', message: 'OK?', question: { text: 'OK?', options: [] }, ts: 't1' });
fire('interview_answered', { node_id: 'plan_gate', question_id: 'g1', detail: { label: '[R] Revise plan', note: 'prefer a smaller first commit' }, ts: 't2' });
process.stdout.write(JSON.stringify({
  answered: read('(feed.find(e => e.t==="gate")||{}).answered'),
  label: read('((feed.find(e => e.t==="gate")||{}).answer||{}).label'),
  note: read('((feed.find(e => e.t==="gate")||{}).answer||{}).note'),
}));
`)
	for _, want := range []string{`"answered":true`, `"label":"[R] Revise plan"`, `"note":"prefer a smaller first commit"`} {
		if !strings.Contains(out, want) {
			t.Errorf("gate answer not taken from event detail, missing %q:\n%s", want, out)
		}
	}
}

// TestFeedIdempotentReplay: a mid-run SSE reconnect replays the whole history
// again; the shared replay cursor must keep the feed identical, not double it.
func TestFeedIdempotentReplay(t *testing.T) {
	out := runFeedHarness(t, feedSequence+`
const first = read('feed.length');
if (onopenCb) onopenCb();
`+feedSequence+`
process.stdout.write(JSON.stringify({ first, second: read('feed.length') }));
`)
	if !strings.Contains(out, `"first":`) {
		t.Fatalf("no feed length reported:\n%s", out)
	}
	// first === second: the replay added nothing.
	var first, second string
	if i := strings.Index(out, `"first":`); i >= 0 {
		first = out[i+len(`"first":`):]
		first = first[:strings.IndexAny(first, ",}")]
	}
	if i := strings.Index(out, `"second":`); i >= 0 {
		second = out[i+len(`"second":`):]
		second = second[:strings.IndexAny(second, ",}")]
	}
	if first != second || first == "" {
		t.Errorf("reconnect replay changed the feed: first=%s second=%s\n%s", first, second, out)
	}
}
