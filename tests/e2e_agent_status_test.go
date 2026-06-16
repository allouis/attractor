package attractor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/backend"
	"github.com/fabro/attractor/internal/backend/fake"
	"github.com/fabro/attractor/internal/engine"
)

// TestAgentStatus_AgentCanFailStage exercises the status-file contract
// from Attractor spec §4.5 + Appendix C: when the agent writes
// {stage_dir}/status.json during its turn, the codergen handler uses
// it as the stage outcome instead of synthesising SUCCESS.
func TestAgentStatus_AgentCanFailStage(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		stuck [prompt="no context to work with"]
		fix   [prompt="route here on fail", retry_target="stuck"]
		done  [shape=Msquare]
		start -> stuck
		stuck -> done [condition="outcome=success"]
		stuck -> fix  [condition="outcome=fail"]
		fix -> done
	}`
	be := fake.New()
	// FakeBackend that writes a FAIL status.json before returning so we
	// exercise the agent-self-report code path. The text response is
	// what would still go into response.md for the audit trail.
	be.SetSequence("stuck", fake.Step{Outcome: nil, Text: "agent says: stuck"})
	be.SetSequence("fix", fake.Step{Text: "fix landed"})

	// We need a hook that runs *during* stuck.Run() to write the
	// status.json on the agent's behalf. The FakeBackend doesn't have
	// that hook today, so wire a one-off CodergenBackend wrapper that
	// writes the status file then delegates to the fake.
	wrapper := backend.Func(func(env engine.HandlerEnv, prompt string) (backend.Result, error) {
		if env.Node.ID == "stuck" {
			stageDir := filepath.Join(env.LogsRoot, env.Node.ID)
			must(t, os.MkdirAll(stageDir, 0o755))
			payload := map[string]any{
				"outcome":        "fail",
				"failure_reason": "no prior context available; need bug + scoped test cmd",
			}
			data, _ := json.Marshal(payload)
			must(t, os.WriteFile(filepath.Join(stageDir, "status.json"), data, 0o644))
		}
		return be.Run(env, prompt)
	})

	out, _, logs := runFixture(t, src, wrapper, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("pipeline should reach SUCCESS via fail edge; got %s reason=%q", out.Status, out.FailureReason)
	}
	// Confirm the fix branch ran (i.e. the engine routed via the fail
	// edge rather than the success edge).
	if _, err := os.Stat(filepath.Join(logs, "fix", "status.json")); err != nil {
		t.Fatalf("expected fix branch to execute via fail edge: %v", err)
	}
	// Confirm stuck's status.json reflects the agent's FAIL reason.
	data, err := os.ReadFile(filepath.Join(logs, "stuck", "status.json"))
	must(t, err)
	if !strings.Contains(string(data), "no prior context available") {
		t.Fatalf("agent failure_reason missing from final status.json: %s", data)
	}
}

// TestAgentStatus_NoSelfReportFallsBackToSuccess confirms that the
// default behaviour is unchanged when the agent doesn't write a
// status.json — text response gets wrapped in SUCCESS.
func TestAgentStatus_NoSelfReportFallsBackToSuccess(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	be := fake.New()
	be.SetText("work", "all good")
	out, _, logs := runFixture(t, src, be, nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s", out.Status)
	}
	st := readStatus(t, logs, "work")
	if st.Status != engine.StatusSuccess {
		t.Fatalf("default outcome = %s, want success", st.Status)
	}
}
