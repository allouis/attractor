package attractor_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
)

// Spans are first-class (amendment A4): every execution attempt gets
// its own directory at the run root, named by its span identity —
// {node_id}@v{visit}.a{attempt}/ — derived forward from the identity
// the event log already carries. No mirrors, no fallbacks: the same
// rule live, archived, and hub-served.

// A cold retry (attempt 2) must not destroy attempt 1's evidence, and
// EVERY terminal attempt gets a canonical engine-resolved status.json
// — including the retry attempt itself.
func TestSpanDirs_AttemptsPreservedWithCanonicalStatus(t *testing.T) {
	src := `digraph t {
		default_max_retries=1
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	be := fake.New()
	be.SetSequence("work",
		fake.Step{Err: backend.Transient(errors.New("connection reset"))},
		fake.Step{Text: "second attempt wins"},
	)
	out, _, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %+v", out)
	}

	// Attempt 1: preserved, with a canonical retry status.
	a1 := filepath.Join(logs, "work@v1.a1")
	data, err := os.ReadFile(filepath.Join(a1, "status.json"))
	if err != nil {
		t.Fatalf("attempt 1 canonical status missing: %v", err)
	}
	var st1 engine.Outcome
	must(t, json.Unmarshal(data, &st1))
	if st1.StatusString != "retry" || !strings.Contains(st1.FailureReason, "connection reset") {
		t.Fatalf("attempt 1 status wrong: %+v", st1)
	}

	// Attempt 2: its own dir, success status, its own response.
	a2 := filepath.Join(logs, "work@v1.a2")
	resp, err := os.ReadFile(filepath.Join(a2, "response.md"))
	if err != nil {
		t.Fatalf("attempt 2 response missing: %v", err)
	}
	if string(resp) != "second attempt wins" {
		t.Fatalf("attempt 2 response = %q", resp)
	}
	data2, err := os.ReadFile(filepath.Join(a2, "status.json"))
	must(t, err)
	if !strings.Contains(string(data2), `"success"`) {
		t.Fatalf("attempt 2 status = %s", data2)
	}

	// No node-root mirror files: span dirs are the only storage.
	if _, err := os.Stat(filepath.Join(logs, "work", "response.md")); !os.IsNotExist(err) {
		t.Fatalf("node-root mirror still written (err=%v)", err)
	}
}

// Graph re-entry (a new visit) gets a fresh span dir under the same rule.
func TestSpanDirs_VisitsGetFreshDirs(t *testing.T) {
	src := `digraph t {
		max_node_visits=5
		max_repeated_failures=0
		start [shape=Mdiamond]
		work [prompt="w"]
		fix  [prompt="f"]
		done [shape=Msquare]
		start -> work
		work -> done [condition="outcome=success"]
		work -> fix  [condition="outcome=fail"]
		fix -> work
	}`
	be := fake.New()
	be.SetSequence("work",
		fake.Step{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: "first visit fails"}},
		fake.Step{Text: "second visit ok"},
	)
	be.SetText("fix", "fixed")
	out, _, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %+v", out)
	}
	// Visit 1 (failed) has a canonical FAIL status; visit 2 succeeded.
	d1, err := os.ReadFile(filepath.Join(logs, "work@v1.a1", "status.json"))
	must(t, err)
	if !strings.Contains(string(d1), `"fail"`) {
		t.Fatalf("v1 status = %s", d1)
	}
	d2, err := os.ReadFile(filepath.Join(logs, "work@v2.a1", "response.md"))
	must(t, err)
	if string(d2) != "second visit ok" {
		t.Fatalf("v2 response = %q", d2)
	}
}

// The agent's own status.json is preserved verbatim as
// agent-status.json when the engine writes its canonical resolution
// over status.json — the audit trail keeps the agent's exact words.
func TestSpanDirs_AgentStatusPreserved(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	be := backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		_ = os.MkdirAll(env.Stage.Root(), 0o755)
		_ = os.WriteFile(filepath.Join(env.Stage.Root(), "status.json"),
			[]byte(`{"outcome":"success","notes":"agent wrote this","extra_field":"kept"}`), 0o644)
		return backend.Result{ResponseText: "ok"}, nil
	})
	out, _, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %+v", out)
	}
	raw, err := os.ReadFile(filepath.Join(logs, "work@v1.a1", "agent-status.json"))
	if err != nil {
		t.Fatalf("agent-status.json missing: %v", err)
	}
	if !strings.Contains(string(raw), "extra_field") {
		t.Fatalf("agent's exact words lost: %s", raw)
	}
	canon, err := os.ReadFile(filepath.Join(logs, "work@v1.a1", "status.json"))
	must(t, err)
	if !strings.Contains(string(canon), `"success"`) {
		t.Fatalf("canonical status wrong: %s", canon)
	}
}

// A failed tool node also gets a canonical status document (failures
// used to leave the dir without one).
func TestSpanDirs_FailedToolHasStatus(t *testing.T) {
	src := `digraph t {
		max_repeated_failures=0
		start [shape=Mdiamond]
		check [type="tool", tool_command="echo diag; exit 1"]
		done [shape=Msquare]
		start -> check -> done
	}`
	out, _, logs := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("want fail, got %+v", out)
	}
	data, err := os.ReadFile(filepath.Join(logs, "check@v1.a1", "status.json"))
	if err != nil {
		t.Fatalf("failed tool has no canonical status: %v", err)
	}
	if !strings.Contains(string(data), `"fail"`) || !strings.Contains(string(data), "diag") {
		t.Fatalf("tool status wrong: %s", data)
	}
}

// Stage B: parallel branches run through the SAME engine executor as
// every other node (HandlerEnv.ExecuteNode). That gives them what the
// handler-side mini-executor silently lacked: D1 retries, §4.12 panic
// recovery, correct visit numbers, and span-rule storage.

const spanFanSrc = `digraph t {
	default_max_retries=1
	max_repeated_failures=0
	start [shape=Mdiamond]
	fan [shape=component]
	a [prompt="branch a"]
	b [prompt="branch b"]
	synth [prompt="merge"]
	done [shape=Msquare]
	start -> fan
	fan -> a
	fan -> b
	a -> synth
	b -> synth
	synth -> done
}`

// A transient branch failure retries instead of silently dropping the
// branch from the fan-out.
func TestSpanBranches_TransientFailureRetries(t *testing.T) {
	be := fake.New()
	be.SetSequence("a",
		fake.Step{Err: backend.Transient(errors.New("429 too many requests"))},
		fake.Step{Text: "a recovered"},
	)
	be.SetText("b", "b ok")
	out, _, logs := runFixture(t, spanFanSrc, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %+v", out)
	}
	if got := be.CallCount("a"); got != 2 {
		t.Fatalf("branch a called %d times, want 2 (transient retry)", got)
	}
	// Both attempts stored under the span rule at the run root.
	if _, err := os.Stat(filepath.Join(logs, "a@v1.a1", "status.json")); err != nil {
		t.Fatalf("branch attempt 1 span dir missing: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(logs, "a@v1.a2", "response.md")); err != nil || string(data) != "a recovered" {
		t.Fatalf("branch attempt 2 wrong: %v %q", err, data)
	}
}

// A panicking branch fails its branch, not the process.
func TestSpanBranches_PanicRecovered(t *testing.T) {
	be := backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		if env.Node.ID == "a" {
			panic("branch exploded")
		}
		return backend.Result{ResponseText: "ok " + env.Node.ID}, nil
	})
	out, _, _ := runFixture(t, spanFanSrc, be, nil)
	// wait_all with one failed branch → partial success; the run then
	// proceeds (synth still runs). The point: no process crash.
	if out.Status == engine.StatusFail && strings.Contains(out.FailureReason, "panic") == false {
		t.Logf("outcome: %+v", out)
	}
}

// Re-entering the fan-out (a fix loop re-reviewing) gives branches a
// SECOND visit — their spans and dirs must say v2, not v1 again.
func TestSpanBranches_RevisitIncrementsVisit(t *testing.T) {
	src := `digraph t {
		max_node_visits=5
		max_repeated_failures=0
		start [shape=Mdiamond]
		fan [shape=component]
		lens [prompt="review"]
		synth [prompt="merge", require_status="true"]
		fixit [prompt="fix"]
		done [shape=Msquare]
		start -> fan
		fan -> lens
		lens -> synth
		synth -> done  [condition="outcome=success"]
		synth -> fixit [condition="outcome=fail"]
		fixit -> fan
	}`
	be := backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		if env.Node.ID == "synth" {
			// First review FAILs, second passes — both via the contract.
			verdict := `{"outcome":"fail","failure_reason":"blocking"}`
			if _, err := os.Stat(filepath.Join(filepath.Dir(env.Stage.Root()), "synth@v1.a1", "agent-status.json")); err == nil {
				verdict = `{"outcome":"success"}`
			}
			_ = os.MkdirAll(env.Stage.Root(), 0o755)
			_ = os.WriteFile(filepath.Join(env.Stage.Root(), "status.json"), []byte(verdict), 0o644)
		}
		return backend.Result{ResponseText: "ok " + env.Node.ID}, nil
	})
	out, events, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("run failed: %+v", out)
	}
	// The lens ran twice, in two distinct visits — dirs and events agree.
	if _, err := os.Stat(filepath.Join(logs, "lens@v1.a1")); err != nil {
		t.Fatalf("lens v1 span missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(logs, "lens@v2.a1")); err != nil {
		t.Fatalf("lens v2 span missing (branch visits stuck at 1?): %v", err)
	}
	visits := map[int]bool{}
	for _, ev := range events {
		if ev.NodeID == "lens" && ev.Kind == engine.EventStageStarted {
			visits[ev.Visit] = true
		}
	}
	if !visits[1] || !visits[2] {
		t.Fatalf("lens events carry visits %v, want 1 and 2", visits)
	}
}
