package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// localLauncher runs a submitted pipeline as a child `attractor run
// --report-to` process on the same host. The child phones its events and
// artifacts home over loopback HTTP — the same contract the VM launcher
// uses, so local and vm behave identically (decision D3/D5).
type localLauncher struct {
	// bin is the attractor binary the child is spawned from. Empty resolves
	// to the running executable (os.Executable).
	bin string
	// logsBase is where each child's throwaway local run dir is created.
	// Empty uses the OS temp dir. The daemon keeps its own copy via ingest
	// + artifact upload, so this dir is not the source of truth.
	logsBase string
}

// NewLocalLauncher returns a Launcher that runs each pipeline as a child
// `attractor run --report-to` process spawned from the running executable.
func NewLocalLauncher() Launcher { return localLauncher{} }

func (l localLauncher) binary() (string, error) {
	if l.bin != "" {
		return l.bin, nil
	}
	return os.Executable()
}

// Launch materializes the run's source, spawns the child, and blocks until
// it exits (holding the dispatcher slot). If the child exits without the
// run having reached a terminal state via phone-home, the run is failed.
func (l localLauncher) Launch(run *Run, reportURL string) error {
	bin, err := l.binary()
	if err != nil {
		return fmt.Errorf("locate attractor binary: %w", err)
	}
	childLogs, err := os.MkdirTemp(l.logsBase, "attractor-local-"+run.ID+"-")
	if err != nil {
		return fmt.Errorf("child logs dir: %w", err)
	}
	srcPath := filepath.Join(childLogs, "source.dot")
	if err := os.WriteFile(srcPath, []byte(run.Source()), 0o644); err != nil {
		return fmt.Errorf("write source: %w", err)
	}

	args := localRunArgs(run, reportURL, srcPath, childLogs)
	cmd := exec.Command(bin, args...)
	cmd.Dir = run.cwd
	cmd.Stdout = os.Stderr // child console → daemon stderr for debugging
	cmd.Stderr = os.Stderr

	runErr := cmd.Run()
	if !run.isTerminal() {
		reason := "child exited without reporting completion"
		if runErr != nil {
			reason = fmt.Sprintf("child process error: %v", runErr)
		}
		run.failCrashed(reason)
	}
	return runErr
}

// localRunArgs builds the argv for the child `attractor run` invocation.
// Pure (no side effects) so it can be unit-tested without spawning.
func localRunArgs(run *Run, reportURL, srcPath, childLogs string) []string {
	args := []string{
		"run",
		"--report-to", reportURL,
		"--run-id", run.ID,
		"--report-token", run.Token(),
		"--logs", childLogs,
	}
	if run.cwd != "" {
		args = append(args, "--base-dir", run.cwd, "--cwd", run.cwd)
	}
	// Deterministic -var order for stable argv (testability).
	keys := make([]string, 0, len(run.initialContext))
	for k := range run.initialContext {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-var", k+"="+run.initialContext[k])
	}
	args = append(args, srcPath)
	return args
}
