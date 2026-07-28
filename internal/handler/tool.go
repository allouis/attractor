package handler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/allouis/attractor/internal/engine"
)

// Tool executes an external command configured on the node and returns
// the captured stdout. Spec §4.10. The shell used is `/bin/sh -c <cmd>`
// so the command may contain pipes / redirects.
type Tool struct{}

// Execute runs the configured command. The node's working directory
// defaults to the run logs root; the command itself controls cwd via
// `cd` or absolute paths.
func (Tool) Execute(env engine.HandlerEnv) engine.Outcome {
	cmd := env.Node.Attrs["tool_command"]
	if cmd == "" {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "tool: no tool_command specified"}
	}
	// Interpolate $context.* / $goal from the live context (spec §4.5,
	// generalised to tool_command — the one documented deviation) before
	// handing the command to the shell. Only $context.* is touched, so
	// shell syntax ($HOME, $(…)) survives; an undefined key fails fast so
	// no mangled command reaches /bin/sh.
	expanded, err := expandContext(cmd, env.Context)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "tool: " + err.Error()}
	}
	cmd = expanded

	ctx := context.Background()
	if d, ok := env.Node.Duration("timeout"); ok && d > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	exe := exec.CommandContext(ctx, "/bin/sh", "-c", cmd)
	stageDir := filepath.Join(env.LogsRoot, env.Node.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("tool: mkdir stage dir: %v", err)}
	}
	// Run from env.Cwd when set; fall back to the stage dir so
	// short-lived commands still have a writable scratch directory.
	if env.Cwd != "" {
		exe.Dir = env.Cwd
	} else {
		exe.Dir = stageDir
	}

	var stdout, stderr bytes.Buffer
	exe.Stdout = &stdout
	exe.Stderr = &stderr
	err = exe.Run()

	// Persist stdout/stderr to the stage dir regardless of cwd.
	_ = os.WriteFile(filepath.Join(stageDir, "stdout.txt"), stdout.Bytes(), 0o644)
	_ = os.WriteFile(filepath.Join(stageDir, "stderr.txt"), stderr.Bytes(), 0o644)

	if err != nil {
		return engine.Outcome{
			Status:        engine.StatusFail,
			FailureReason: fmt.Sprintf("tool: %v: %s", err, truncate(stderr.String(), 200)),
			ContextUpdates: map[string]string{
				"tool.output":    stdout.String(),
				"tool.stderr":    stderr.String(),
				"tool.exit_code": exitCodeString(err),
			},
		}
	}
	return engine.Outcome{
		Status: engine.StatusSuccess,
		Notes:  "Tool completed: " + cmd,
		ContextUpdates: map[string]string{
			"tool.output":    stdout.String(),
			"tool.exit_code": "0",
		},
	}
}

func exitCodeString(err error) string {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Sprintf("%d", ee.ExitCode())
	}
	return "-1"
}
