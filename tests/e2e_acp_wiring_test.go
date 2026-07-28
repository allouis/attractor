package attractor_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/cli"
	"github.com/allouis/attractor/internal/lint"
)

// TestCLI_ACPBackendEndToEnd runs a whole pipeline through the ACP
// backend, agent supplied via --acp-cmd.
func TestCLI_ACPBackendEndToEnd(t *testing.T) {
	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "acp",
		"--acp-cmd", fakeACPCommand(t),
		"--logs", logsRoot,
		"../testdata/pipelines/smoke.dot",
	})
	must(t, err)

	resp, err := os.ReadFile(filepath.Join(logsRoot, "plan", "response.md"))
	must(t, err)
	if !strings.Contains(string(resp), "echo:") {
		t.Fatalf("plan response.md should carry agent output, got %q", resp)
	}
}

// TestCLI_ACPBackendWithoutCommandFails: --backend acp with no command
// from any source fails the run with a hint.
func TestCLI_ACPBackendWithoutCommandFails(t *testing.T) {
	logsRoot := t.TempDir()
	err := cli.Run([]string{
		"--backend", "acp",
		"--logs", logsRoot,
		"../testdata/pipelines/smoke.dot",
	})
	if err == nil {
		t.Fatal("expected failure without an agent command")
	}
	if !strings.Contains(err.Error(), "acp") {
		t.Fatalf("error should point at the acp configuration, got %v", err)
	}
}

func TestLint_ACPBackendTypeIsKnown(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		plan [type="codergen.acp", prompt="plan it", acp_command="my-agent"]
		done [shape=Msquare]
		start -> plan -> done
	}`)
	diags := lint.Validate(g)
	if hasDiag(diags, "codergen_type_known", lint.Warning) {
		t.Fatalf("codergen.acp should be a known backend type: %+v", diags)
	}
}

func TestLint_ACPCommandMissingWarns(t *testing.T) {
	g := build(t, `digraph g {
		start [shape=Mdiamond]
		plan [type="codergen.acp", prompt="plan it"]
		done [shape=Msquare]
		start -> plan -> done
	}`)
	diags := lint.Validate(g)
	if !hasDiag(diags, "acp_command_missing", lint.Warning) {
		t.Fatalf("expected acp_command_missing warning, got %+v", diags)
	}
}

func TestLint_ACPCommandOnGraphSuppressesWarning(t *testing.T) {
	g := build(t, `digraph g {
		acp_command = "my-agent"
		start [shape=Mdiamond]
		plan [type="codergen.acp", prompt="plan it"]
		done [shape=Msquare]
		start -> plan -> done
	}`)
	diags := lint.Validate(g)
	if hasDiag(diags, "acp_command_missing", lint.Warning) {
		t.Fatalf("graph-level acp_command should satisfy the rule: %+v", diags)
	}
}
