package attractor_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/interviewer"
)

const linearDOT = `digraph linear {
    goal = "Sort a list"
    start [shape=Mdiamond]
    plan [prompt="Plan: $goal"]
    impl [prompt="Implement plan"]
    done [shape=Msquare]
    start -> plan -> impl -> done
}`

func TestEngine_LinearPipelineWithFakeBackend(t *testing.T) {
	be := fake.New()
	be.SetText("plan", "PLAN: split sort into ranges")
	be.SetText("impl", "IMPL: merged sort, tests pass")
	out, events, logs := runFixture(t, linearDOT, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
	// prompt/response/status artifacts present for both codergen stages.
	for _, id := range []string{"plan", "impl"} {
		dir := filepath.Join(logs, id)
		for _, f := range []string{"prompt.md", "response.md", "status.json"} {
			if !fileExists(t, filepath.Join(dir, f)) {
				t.Fatalf("missing %s/%s", id, f)
			}
		}
	}
	planStatus := readStatus(t, logs, "plan")
	if planStatus.Status != engine.StatusSuccess {
		t.Fatalf("plan status = %s", planStatus.Status)
	}
	if !strings.Contains(planStatus.ContextUpdates["last_response"], "PLAN") {
		t.Fatalf("last_response not propagated: %q", planStatus.ContextUpdates["last_response"])
	}
	if !fileExists(t, filepath.Join(logs, "checkpoint.json")) {
		t.Fatal("checkpoint.json missing")
	}
	if !fileExists(t, filepath.Join(logs, "manifest.json")) {
		t.Fatal("manifest.json missing")
	}
	// pipeline_started and pipeline_completed events fired.
	gotStart, gotEnd := false, false
	for _, ev := range events {
		if ev.Kind == engine.EventPipelineStarted {
			gotStart = true
		}
		if ev.Kind == engine.EventPipelineCompleted {
			gotEnd = true
		}
	}
	if !gotStart || !gotEnd {
		t.Fatalf("lifecycle events missing: start=%v end=%v", gotStart, gotEnd)
	}
}

func TestEngine_RollsUpUsageOnPipelineCompleted(t *testing.T) {
	// A backend that emits one usage event per stage; the engine should
	// sum them onto the terminal pipeline event (service-spec §6).
	be := backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		env.Emit(engine.Event{
			Kind:   engine.EventUsage,
			NodeID: env.Node.ID,
			Usage:  &engine.Usage{InputTokens: 100, OutputTokens: 20},
		})
		return backend.Result{ResponseText: "ok"}, nil
	})
	_, events, _ := runFixture(t, linearDOT, be, nil)

	var completed *engine.Event
	for i, ev := range events {
		if ev.Kind == engine.EventPipelineCompleted {
			completed = &events[i]
		}
	}
	if completed == nil {
		t.Fatal("no pipeline_completed event")
	}
	if completed.Usage == nil {
		t.Fatalf("pipeline_completed should carry the run usage rollup, got %+v", completed)
	}
	// linearDOT has two codergen stages (plan, impl).
	if completed.Usage.InputTokens != 200 || completed.Usage.OutputTokens != 40 {
		t.Fatalf("run usage rollup wrong: %+v", completed.Usage)
	}
}

func TestEngine_PromptVariableExpansion(t *testing.T) {
	be := fake.New()
	runFixture(t, linearDOT, be, nil)
	calls := be.Calls()
	var planPrompt string
	for _, c := range calls {
		if c.NodeID == "plan" {
			planPrompt = c.Prompt
		}
	}
	if !strings.Contains(planPrompt, "Sort a list") {
		t.Fatalf("plan prompt not expanded: %q", planPrompt)
	}
}

func TestEngine_ConditionalRouting(t *testing.T) {
	src := `digraph branch {
		start [shape=Mdiamond]
		gate [prompt="evaluate"]
		ok [prompt="happy"]
		bad [prompt="sad"]
		done [shape=Msquare]
		start -> gate
		gate -> ok [condition="outcome=success"]
		gate -> bad [condition="outcome=fail"]
		ok -> done
		bad -> done
	}`
	be := fake.New()
	out, _, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s", out.Status)
	}
	if !fileExists(t, filepath.Join(logs, "ok", "status.json")) {
		t.Fatal("expected ok path taken")
	}
	if fileExists(t, filepath.Join(logs, "bad", "status.json")) {
		t.Fatal("did not expect bad path taken")
	}
}

func TestEngine_PreferredLabelRouting(t *testing.T) {
	src := `digraph p {
		start [shape=Mdiamond]
		choose [prompt="pick"]
		left [prompt="L"]
		right [prompt="R"]
		done [shape=Msquare]
		start -> choose
		choose -> left [label="Left"]
		choose -> right [label="Right"]
		left -> done
		right -> done
	}`
	be := fake.New()
	be.SetSequence("choose", fake.Step{Outcome: &engine.Outcome{
		Status:         engine.StatusSuccess,
		PreferredLabel: "Right",
	}})
	out, _, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s", out.Status)
	}
	if !fileExists(t, filepath.Join(logs, "right", "status.json")) {
		t.Fatal("expected right edge taken via preferred_label")
	}
}

func TestEngine_RetryOnFailure(t *testing.T) {
	src := `digraph r {
		start [shape=Mdiamond]
		flaky [prompt="x", max_retries=3]
		done [shape=Msquare]
		start -> flaky -> done
	}`
	be := fake.New()
	be.SetSequence("flaky",
		fake.Step{Outcome: &engine.Outcome{Status: engine.StatusRetry, FailureReason: "transient"}},
		fake.Step{Outcome: &engine.Outcome{Status: engine.StatusRetry, FailureReason: "transient"}},
		fake.Step{Outcome: &engine.Outcome{Status: engine.StatusSuccess}},
	)
	out, events, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("expected SUCCESS after retries, got %s", out.Status)
	}
	if be.CallCount("flaky") != 3 {
		t.Fatalf("expected 3 attempts, got %d", be.CallCount("flaky"))
	}
	gotRetry := false
	for _, ev := range events {
		if ev.Kind == engine.EventStageRetrying {
			gotRetry = true
		}
	}
	if !gotRetry {
		t.Fatal("expected at least one stage_retrying event")
	}
}

func TestEngine_GoalGateBlocksExit(t *testing.T) {
	src := `digraph gg {
		start [shape=Mdiamond]
		gate [prompt="g", goal_gate=true, retry_target="plan"]
		plan [prompt="plan"]
		done [shape=Msquare]
		start -> plan -> gate -> done
	}`
	be := fake.New()
	// gate fails first time but the retry_target jumps to plan; second
	// time gate succeeds and exit is reached.
	gateOutcomes := []fake.Step{
		{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: "no diff"}},
		{Outcome: &engine.Outcome{Status: engine.StatusSuccess}},
	}
	be.SetSequence("gate", gateOutcomes...)
	out, _, _ := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("expected SUCCESS after retry path, got %s reason=%q", out.Status, out.FailureReason)
	}
	if be.CallCount("plan") != 2 {
		t.Fatalf("expected 2 plan invocations (initial + retry), got %d", be.CallCount("plan"))
	}
}

func TestEngine_WaitHumanWithQueueInterviewer(t *testing.T) {
	src := `digraph wh {
		start [shape=Mdiamond]
		gate [shape=hexagon, label="Approve?"]
		yes [prompt="proceed"]
		no [prompt="halt"]
		done [shape=Msquare]
		start -> gate
		gate -> yes [label="Yes"]
		gate -> no [label="No"]
		yes -> done
		no -> done
	}`
	q := interviewer.NewQueue(interviewer.Answer{
		Value:          interviewer.AnswerChoice,
		SelectedOption: &interviewer.Option{Key: "Y", Label: "Yes"},
	})
	be := fake.New()
	out, _, logs := runFixture(t, src, be, q)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s", out.Status)
	}
	if !fileExists(t, filepath.Join(logs, "yes", "status.json")) {
		t.Fatal("expected yes branch taken")
	}
}

func TestEngine_FailureFailEdgeRouting(t *testing.T) {
	src := `digraph f {
		start [shape=Mdiamond]
		work [prompt="x"]
		fix [prompt="repair"]
		done [shape=Msquare]
		start -> work
		work -> fix [condition="outcome=fail"]
		work -> done [condition="outcome=success"]
		fix -> done
	}`
	be := fake.New()
	be.SetSequence("work", fake.Step{Outcome: &engine.Outcome{Status: engine.StatusFail, FailureReason: "boom"}})
	out, _, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		// Final outcome: fix succeeds, then done. Pipeline ends SUCCESS.
		t.Fatalf("expected SUCCESS via fail edge → fix; got %s", out.Status)
	}
	if !fileExists(t, filepath.Join(logs, "fix", "status.json")) {
		t.Fatal("expected fail edge to route through fix")
	}
}

func TestEngine_ResumeFromCheckpoint(t *testing.T) {
	src := linearDOT
	logsRoot := t.TempDir()
	be1 := fake.New()
	// First run: plan succeeds, impl panics (synthesized via fake error).
	be1.SetSequence("plan", fake.Step{Text: "PLAN"})
	be1.SetSequence("impl", fake.Step{Err: errCrash})
	out1, _ := runFixtureIn(t, src, be1, nil, logsRoot)
	if out1.Status != engine.StatusFail {
		t.Fatalf("first run should fail; got %s", out1.Status)
	}
	if !fileExists(t, filepath.Join(logsRoot, "plan", "status.json")) {
		t.Fatal("plan should have succeeded before crash")
	}
	if fileExists(t, filepath.Join(logsRoot, "impl", "status.json")) {
		t.Fatal("impl status should not exist after crash")
	}

	// Second run with same logs root: engine should skip plan and finish.
	be2 := fake.New()
	be2.SetText("impl", "IMPL: now working")
	out2, _ := runFixtureIn(t, src, be2, nil, logsRoot)
	if out2.Status != engine.StatusSuccess {
		t.Fatalf("resumed run status=%s reason=%q", out2.Status, out2.FailureReason)
	}
	// plan should NOT have been called again.
	if be2.CallCount("plan") != 0 {
		t.Fatalf("plan re-executed on resume: %d calls", be2.CallCount("plan"))
	}
	if be2.CallCount("impl") != 1 {
		t.Fatalf("impl should run once on resume, got %d", be2.CallCount("impl"))
	}
}

func TestEngine_EdgeSelectionWeightTiebreak(t *testing.T) {
	src := `digraph w {
		start [shape=Mdiamond]
		fork [prompt="x"]
		a [prompt="A"]
		b [prompt="B"]
		done [shape=Msquare]
		start -> fork
		fork -> a [weight=1]
		fork -> b [weight=5]
		a -> done
		b -> done
	}`
	be := fake.New()
	_, _, logs := runFixture(t, src, be, nil)
	if !fileExists(t, filepath.Join(logs, "b", "status.json")) {
		t.Fatal("weight=5 edge should win")
	}
	if fileExists(t, filepath.Join(logs, "a", "status.json")) {
		t.Fatal("lower-weight edge should be skipped")
	}
}

func TestEngine_EdgeSelectionLexicalTiebreak(t *testing.T) {
	src := `digraph l {
		start [shape=Mdiamond]
		fork [prompt="x"]
		aa [prompt="A"]
		bb [prompt="B"]
		done [shape=Msquare]
		start -> fork
		fork -> bb
		fork -> aa
		aa -> done
		bb -> done
	}`
	be := fake.New()
	_, _, logs := runFixture(t, src, be, nil)
	if !fileExists(t, filepath.Join(logs, "aa", "status.json")) {
		t.Fatal("aa wins lexically when weights tie")
	}
}

// errCrash simulates a backend failure for the resume test.
var errCrash = &simErr{"simulated crash"}

type simErr struct{ msg string }

func (e *simErr) Error() string { return e.msg }
