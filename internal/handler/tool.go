package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// toolWaitDelay bounds how long exec.Cmd.Wait keeps copying stdout/stderr after
// the command's process has exited (or been killed). A tool_command that
// backgrounds a grandchild (a test runner spawning worker processes) leaves
// that child holding the stdout pipe open after the shell exits; without a
// bound, Wait blocks on the pipe copy forever — the failure mode that wedged a
// run for hours. After this delay Wait abandons the copy, closes the pipes, and
// returns, so a stuck stage fails instead of hanging the engine.
const toolWaitDelay = 5 * time.Second

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
	expanded, err := env.Context.Expand(cmd)
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
	// Run the shell in its own process group (Setpgid) and, on timeout, kill the
	// whole group (negative pid) rather than just the shell. A test runner
	// spawns worker grandchildren; killing only the shell orphans them still
	// holding the stdout pipe, which blocks Wait. Killing the group reaps the
	// tree so the pipe closes promptly. WaitDelay below is the backstop for a
	// child that escapes the group (setsid) — it bounds the pipe copy regardless.
	exe.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	exe.Cancel = func() error {
		if exe.Process == nil {
			return nil
		}
		return syscall.Kill(-exe.Process.Pid, syscall.SIGKILL)
	}
	exe.WaitDelay = toolWaitDelay
	stage := env.Stage
	if stage != nil {
		if err := stage.MkdirAll(); err != nil {
			return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("tool: mkdir stage dir: %v", err)}
		}
	}
	// Run from env.Cwd when set; fall back to the stage dir so
	// short-lived commands still have a writable scratch directory.
	if env.Cwd != "" {
		exe.Dir = env.Cwd
	} else if stage != nil {
		exe.Dir = stage.Root()
	}

	// Stream stdout/stderr to the stage files WHILE the command runs (via
	// io.MultiWriter) so a transport can tail a live check (ui-run-view-v3 P5b);
	// the in-memory buffers stay for FailureReason truncation and the context
	// updates below. The stage files are created before exec so they exist (and
	// grow) from the first byte. Streaming needs an incrementally-writable
	// handle, so this goes through runstore's Create seam rather than the
	// write-once stage.Write used before.
	var stdout, stderr bytes.Buffer
	outW := io.Writer(&stdout)
	errW := io.Writer(&stderr)
	if stage != nil {
		if f, err := stage.Create("stdout.txt"); err == nil {
			defer f.Close()
			outW = io.MultiWriter(f, &stdout)
		}
		if f, err := stage.Create("stderr.txt"); err == nil {
			defer f.Close()
			errW = io.MultiWriter(f, &stderr)
		}
	}
	exe.Stdout = outW
	exe.Stderr = errW
	err = exe.Run()

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
