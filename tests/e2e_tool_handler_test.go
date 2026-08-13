package attractor_test

import (
	"os"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
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
	stdout, err := os.ReadFile(spanPath(t, logs, "gen", "stdout.txt"))
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

// C2: the tool node expands $context.* in tool_command from the live
// context at execute time (the one documented §4.5 deviation).
func TestTool_ExpandsContextInCommand(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		gen [shape=parallelogram, tool_command="printf '%s' $context.repo"]
		done [shape=Msquare]
		start -> gen -> done
	}`
	out, _, logs := runFixtureSeeded(t, src, fake.New(), nil, map[string]string{"repo": "foo/bar"})
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
	st := readStatus(t, logs, "gen")
	if got := st.ContextUpdates["tool.output"]; got != "foo/bar" {
		t.Fatalf("tool.output=%q, want foo/bar", got)
	}
}

// Shell syntax survives — only $context.* is touched. A single-quoted
// $HOME reaches the shell unexpanded by attractor (and stays literal
// under single quotes).
func TestTool_ShellVarReachesShellUnexpanded(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		gen [shape=parallelogram, tool_command="printf '%s' '$HOME'"]
		done [shape=Msquare]
		start -> gen -> done
	}`
	out, _, logs := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusSuccess {
		t.Fatalf("status=%s reason=%q", out.Status, out.FailureReason)
	}
	st := readStatus(t, logs, "gen")
	if got := st.ContextUpdates["tool.output"]; got != "$HOME" {
		t.Fatalf("tool.output=%q, want $HOME", got)
	}
}

// Fail-fast: an undefined $context key fails the node naming the key,
// and the shell never runs.
func TestTool_UndefinedContextKeyFailsNode(t *testing.T) {
	src := `digraph t {
		start [shape=Mdiamond]
		gen [shape=parallelogram, tool_command="printf '%s' $context.nope"]
		done [shape=Msquare]
		start -> gen -> done
	}`
	out, _, logs := runFixture(t, src, fake.New(), nil)
	if out.Status != engine.StatusFail {
		t.Fatalf("expected FAIL on undefined key, got %s", out.Status)
	}
	if !strings.Contains(out.FailureReason, "nope") {
		t.Fatalf("failure reason should name the key: %q", out.FailureReason)
	}
	if _, err := os.Stat(spanPath(t, logs, "gen", "stdout.txt")); !os.IsNotExist(err) {
		t.Fatalf("shell should not have run; stdout.txt exists (err=%v)", err)
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
