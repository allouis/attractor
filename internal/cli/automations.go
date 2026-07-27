package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fabro/attractor/internal/automation"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/setup"
)

// Automations implements `attractor automations list|run <name>`
// (service-spec §5). It runs standalone against ~/.attractor/automations;
// daemon-detection (submit to a running serve) is deliberately deferred.
func Automations(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("automations: expected subcommand: list | run <name>")
	}
	switch args[0] {
	case "list":
		return automationsList()
	case "run":
		if len(args) < 2 {
			return fmt.Errorf("automations run: expected an automation name")
		}
		return automationsRun(args[1])
	default:
		return fmt.Errorf("automations: unknown subcommand %q (valid: list, run)", args[0])
	}
}

// automationsDir is the canonical location for automation TOML files.
func automationsDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".attractor", "automations")
	}
	return filepath.Join(".attractor", "automations")
}

func automationsList() error {
	autos, err := automation.Load(automationsDir())
	if err != nil {
		return err
	}
	if len(autos) == 0 {
		fmt.Println("no automations in", automationsDir())
		return nil
	}
	for _, a := range autos {
		trigger := a.Cron
		if trigger == "" {
			trigger = "(manual)"
		}
		fmt.Printf("%-24s %-16s %s\n", a.Name, trigger, a.Pipeline)
	}
	return nil
}

// automationsRun loads the named automation and executes its pipeline
// standalone, applying the automation's vars and cwd through the shared
// setup path and routing codergen nodes via the provider config.
func automationsRun(name string) error {
	autos, err := automation.Load(automationsDir())
	if err != nil {
		return err
	}
	var found *automation.Automation
	for i := range autos {
		if autos[i].Name == name {
			found = &autos[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("automation %q not found in %s", name, automationsDir())
	}
	dotPath, err := resolvePipelinePath(expandHome(found.Pipeline))
	if err != nil {
		return err
	}
	src, err := os.ReadFile(dotPath)
	if err != nil {
		return err
	}
	prepared, err := setup.Prepare(setup.Options{
		Source:  string(src),
		Vars:    found.Vars,
		BaseDir: filepath.Dir(dotPath),
		Cwd:     found.Cwd,
	})
	if err != nil {
		return err
	}
	logsRoot := filepath.Join(defaultLogsRoot(), engine.NewRunID())
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return err
	}
	cb, err := providerBackend(prepared.Graph)
	if err != nil {
		return err
	}
	return runEngine(prepared, cb, resolveInterviewer("auto"), logsRoot, false)
}

// expandHome replaces a leading ~ with the user's home directory so a
// pipeline reference like ~/.attractor/pipelines/x.dot resolves.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}
