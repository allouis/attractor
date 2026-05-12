package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/backend/fake"
	"github.com/fabro/attractor/internal/engine"
)

func TestTool_RunsShellCommandAndCapturesOutput(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		gen [shape=parallelogram, tool_command="printf 'hello-tool'; printf 'oops' 1>&2"]
		done [shape=Msquare]
		start -> gen -> done
	}`
	out, _, logs := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
	st := readStatus(t, logs, "gen")
	if got := st.ContextUpdates["tool.output"]; got != "hello-tool" {
		t.Fatalf("tool.output=%q, want hello-tool", got)
	}
	if got := st.ContextUpdates["tool.exit_code"]; got != "0" {
		t.Fatalf("tool.exit_code=%q", got)
	}
	stdout, err := os.ReadFile(filepath.Join(logs, "gen", "stdout.txt"))
	must(t, err)
	if string(stdout) != "hello-tool" {
		t.Fatalf("stdout.txt=%q", stdout)
	}
}

func TestTool_NonzeroExitFails(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		gen [shape=parallelogram, tool_command="exit 7"]
		done [shape=Msquare]
		start -> gen -> done
	}`
	out, _, _ := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("expected FAIL on non-zero exit, got %s", out.Status)
	}
	if !strings.Contains(out.FailureReason, "exit status 7") {
		t.Fatalf("failure reason missing exit code: %q", out.FailureReason)
	}
}

func TestTool_MissingCommandFails(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		gen [type="tool"]
		done [shape=Msquare]
		start -> gen -> done
	}`
	out, _, _ := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusFail || !strings.Contains(out.FailureReason, "no tool_command") {
		t.Fatalf("expected FAIL with explicit reason, got %s %q", out.Status, out.FailureReason)
	}
}

func TestTool_RoutesOutputToDownstreamCondition(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		gen [shape=parallelogram, tool_command="printf ok"]
		check [shape=diamond]
		done [shape=Msquare]
		start -> gen -> check
		check -> done [condition="context.tool.output=ok"]
	}`
	out, _, _ := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
}
