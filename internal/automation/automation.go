// Package automation loads saved run configs with triggers from TOML
// files under ~/.attractor/automations/ (service-spec §5). An automation
// pairs a pipeline reference (plus cwd and vars) with a cron trigger; the
// daemon fires it on schedule or on manual request through the same
// admission path as POST /pipelines. File-first, no database.
package automation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fabro/attractor/internal/cron"
	"github.com/fabro/attractor/internal/toml"
)

// Automation is one saved run config + trigger.
type Automation struct {
	// Name is the file stem (nightly-triage.toml -> "nightly-triage").
	Name string
	// Pipeline is the .dot path or bare pipeline name to run.
	Pipeline string
	// Cwd is the working directory passed as the graph-level cwd default.
	Cwd string
	// Vars are $placeholder expansions from the [vars] table.
	Vars map[string]string
	// Cron is the five-field trigger spec from [trigger].cron.
	Cron string
}

// Parse compiles one automation's TOML source. name is the file stem.
// pipeline is required; if cron is set it must be a valid five-field
// spec so a broken schedule is caught at load rather than at fire time.
func Parse(name string, data []byte) (Automation, error) {
	a := Automation{Name: name, Vars: map[string]string{}}
	table := "" // "" top-level, else the current [table] name
	for i, raw := range strings.Split(string(data), "\n") {
		line := toml.StripComment(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			path, err := toml.TableHeader(line)
			if err != nil {
				return Automation{}, fmt.Errorf("automation %q line %d: %w", name, i+1, err)
			}
			table = path[0]
			continue
		}
		key, val, err := toml.ParseKeyValue(line)
		if err != nil {
			return Automation{}, fmt.Errorf("automation %q line %d: %w", name, i+1, err)
		}
		switch table {
		case "":
			switch key {
			case "pipeline":
				a.Pipeline = val
			case "cwd":
				a.Cwd = val
			}
		case "vars":
			a.Vars[key] = val
		case "trigger":
			if key == "cron" {
				a.Cron = val
			}
		}
	}
	if a.Pipeline == "" {
		return Automation{}, fmt.Errorf("automation %q: missing pipeline", name)
	}
	if a.Cron != "" {
		if _, err := cron.Parse(a.Cron); err != nil {
			return Automation{}, fmt.Errorf("automation %q: %w", name, err)
		}
	}
	return a, nil
}

// Load reads every *.toml file in dir and parses it into an Automation.
// A missing directory yields an empty list (not an error): the daemon
// runs fine with no automations configured.
func Load(dir string) ([]Automation, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Automation
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".toml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(ent.Name(), ".toml")
		a, err := Parse(name, data)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}
