package server

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAttachStreamBuildsTimeline drives the real attachStream from the embedded
// index.html (web-ui-v2-spec U3): every streamed event lands in the #tl-rows
// timeline in arrival order, a pipeline_failed event surfaces its reason in the
// #run-failure banner, and a reconnect (full replay from seq 0) does not
// duplicate the rows — the applied/pos cursor holds. Uses node's vm sandbox
// with a fake EventSource/fetch/document, mirroring stage_select_test.go.
func TestAttachStreamBuildsTimeline(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI timeline test")
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
// A fake element whose innerHTML tracks appended rows so the test can assert
// on the incremental single-row appends (insertAdjacentHTML), not just a full
// innerHTML rebuild.
const el = () => ({ innerHTML: '', value: '', textContent: '', scrollTop: 0, scrollHeight: 0,
  querySelectorAll: () => [], querySelector: () => null, firstElementChild: null,
  insertAdjacentHTML(pos, html) { this.innerHTML += html; },
  classList: { toggle() {}, add() {}, remove() {} }, setAttribute() {} });
const document = { getElementById: (id) => (els[id] = els[id] || el()) };

const fetch = (url) => Promise.resolve({ ok: true, status: 200,
  json: () => Promise.resolve({}) });

const sandbox = { window: { addEventListener() {} }, document, location: {},
  console, EventSource: FakeES, fetch };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__setRun = (r) => { currentRun = r; };' +
  '\nglobalThis.__nodeLog = nodeLog;', sandbox);

const fire = (kind, ev) => (listeners[kind] || []).forEach(fn => fn({ data: JSON.stringify(ev) }));
const feed = () => {
  fire('pipeline_started', { ts: '2026-08-02T10:00:00Z' });
  fire('stage_started',    { node_id: 'plan', ts: '2026-08-02T10:00:01Z' });
  // A real run emits stage_progress once per assistant token — thousands of
  // them. They belong to the inspector's live tail, not the high-level
  // timeline, so they must NOT become timeline rows.
  for (let i = 0; i < 500; i++)
    fire('stage_progress', { node_id: 'plan', detail: { kind: 'assistant_delta' }, message: 'TOKENFRAGMENT', ts: '2026-08-02T10:00:01Z' });
  fire('stage_failed',     { node_id: 'plan', message: 'lint blew up', ts: '2026-08-02T10:00:02Z' });
  fire('pipeline_failed',  { message: 'boom the run died', ts: '2026-08-02T10:00:03Z' });
};

sandbox.__setRun('run1');
sandbox.attachStream('run1');
if (onopenCb) onopenCb();
feed();
// Reconnect: the server replays the whole history from seq 0. onopen resets the
// per-connection position; the applied cursor must suppress the re-delivery.
if (onopenCb) onopenCb();
feed();

process.stdout.write(JSON.stringify({
  rows: els['tl-rows'].innerHTML,
  failure: els['run-failure'].innerHTML,
  nodeLog: (sandbox.__nodeLog['plan'] || []).length,
}));
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	result := string(out)

	for _, want := range []string{"pipeline_started", "stage_started", "plan", "stage_failed", "lint blew up", "pipeline_failed"} {
		if !strings.Contains(result, want) {
			t.Errorf("timeline missing %q:\n%s", want, result)
		}
	}
	if !strings.Contains(result, "boom the run died") {
		t.Errorf("failure banner missing the reason:\n%s", result)
	}
	// The per-token stage_progress flood must be excluded from the timeline —
	// neither a stage_progress row nor its token fragments appear.
	if strings.Contains(result, "stage_progress") || strings.Contains(result, "TOKENFRAGMENT") {
		t.Errorf("timeline flooded with stage_progress deltas (should be inspector-only):\n%s", result)
	}
	// ...but stage_progress still drives the inspector's node log (500 chunks
	// per connection, deduped across the reconnect).
	if !strings.Contains(result, `"nodeLog":500`) {
		t.Errorf("stage_progress no longer reaches the node log:\n%s", result)
	}
	// Reconnect replayed every event a second time; the cursor must dedup, so
	// each kind appears exactly once.
	if n := strings.Count(result, "pipeline_started"); n != 1 {
		t.Errorf("reconnect duplicated the timeline: pipeline_started x%d\n%s", n, result)
	}
}

// TestTimelineIsBounded drives the real recordTimelineEvent: a long-running
// pipeline (loops, many stages) must not grow the timeline array or the DOM
// without bound. Past TIMELINE_MAX the oldest events are dropped, keeping the
// most recent window — the part a debugger cares about.
func TestTimelineIsBounded(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available; skipping UI timeline test")
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
const el = () => ({ innerHTML: '', value: '', textContent: '', scrollTop: 0, scrollHeight: 0,
  querySelectorAll: () => [], querySelector: () => null, firstElementChild: null,
  insertAdjacentHTML(pos, html) { this.innerHTML += html; },
  classList: { toggle() {}, add() {}, remove() {} }, setAttribute() {} });
const document = { getElementById: (id) => (els[id] = els[id] || el()) };
const sandbox = { window: { addEventListener() {} }, document, location: {}, console, EventSource: FakeES };
vm.createContext(sandbox);
vm.runInContext(m[1] + '\nglobalThis.__timeline = timeline;\nglobalThis.__MAX = TIMELINE_MAX;', sandbox);

const fire = (kind, ev) => (listeners[kind] || []).forEach(fn => fn({ data: JSON.stringify(ev) }));
sandbox.attachStream('run1');
if (onopenCb) onopenCb();
const MAX = sandbox.__MAX;
const total = MAX + 5;
for (let i = 0; i < total; i++)
  fire('stage_started', { node_id: 'n', message: 'e' + i, ts: '2026-08-02T10:00:00Z' });

process.stdout.write(JSON.stringify({
  max: MAX,
  len: sandbox.__timeline.length,
  oldest: sandbox.__timeline[0].ev.message,
  rowCount: (els['tl-rows'].innerHTML.match(/tl-row/g) || []).length,
}));
`
	out, err := exec.Command("node", "-e", harness, uiPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node harness failed: %v\n%s", err, out)
	}
	var got struct {
		Max      int    `json:"max"`
		Len      int    `json:"len"`
		Oldest   string `json:"oldest"`
		RowCount int    `json:"rowCount"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("bad harness output %q: %v", out, err)
	}
	if got.Max <= 0 {
		t.Fatalf("TIMELINE_MAX not exposed / non-positive: %d", got.Max)
	}
	if got.Len != got.Max {
		t.Errorf("timeline array unbounded: len=%d want cap=%d", got.Len, got.Max)
	}
	if got.RowCount > got.Max {
		t.Errorf("timeline DOM unbounded: %d rows > cap %d", got.RowCount, got.Max)
	}
	// Fired e0..e(MAX+4); the oldest 5 are dropped, so the window starts at e5.
	if got.Oldest != "e5" {
		t.Errorf("bounded timeline kept the wrong window: oldest=%q want %q", got.Oldest, "e5")
	}
}
