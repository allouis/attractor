package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/allouis/attractor/internal/rundir"
	"github.com/allouis/attractor/internal/runview"
)

// runSummary is one row of the `attractor runs` listing, folded from a
// run dir's on-disk state (run.json + events.jsonl). No new bookkeeping.
type runSummary struct {
	// ID is the run directory name — the value `attractor view <dir>`
	// consumes. It is NOT run.json's RunID: those can differ, so the
	// listing keys off the directory to keep find->view working.
	ID        string
	Graph     string
	Status    string // running | success | failed | unknown
	StartedAt time.Time
}

// listRuns folds every immediate subdirectory of root into a summary,
// most-recent-first. It reuses the tolerant run-dir readers, so a
// half-written or malformed run dir degrades to a best-effort row
// (status "unknown") rather than crashing the listing. A missing root
// yields an empty listing, no error.
func listRuns(root string) []runSummary {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []runSummary
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		out = append(out, summarizeRun(filepath.Join(root, e.Name()), e.Name()))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// summarizeRun reads one run dir. The id is always the directory name (so
// it feeds `attractor view <dir>`). A run.json that is missing, malformed,
// or unreadable — or an events.jsonl that is corrupt/unreadable — yields
// status "unknown" rather than a misleading "running"; a zero started-at
// falls back to the dir's mtime so ordering stays useful.
func summarizeRun(dir, name string) runSummary {
	s := runSummary{ID: name}
	m, err := rundir.ReadManifest(dir)
	if err != nil {
		// run.json missing / malformed / unreadable: best-effort row.
		s.Status = "unknown"
	} else {
		s.Graph = m.GraphName
		s.StartedAt = m.StartedAt
		if events, eerr := rundir.ReadEvents(dir, 0); eerr != nil {
			// A corrupt/unreadable event log cannot be trusted to say the
			// run is still running, so surface uncertainty as unknown.
			s.Status = "unknown"
		} else {
			s.Status = displayStatus(runview.Document(m, events).Status)
		}
	}
	if s.StartedAt.IsZero() {
		if fi, serr := os.Stat(dir); serr == nil {
			s.StartedAt = fi.ModTime()
		}
	}
	return s
}

// displayStatus maps the API status vocabulary (running/completed/failed)
// to the listing's wording (running/success/failed); anything else passes
// through (defensive — an unexpected value is shown, not swallowed).
func displayStatus(status string) string {
	if status == "completed" {
		return "success"
	}
	return status
}

// Runs lists the local runs under the runs root, most-recent-first.
func Runs(args []string) error {
	fs := flag.NewFlagSet("runs", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "runs root to list (default: $XDG_DATA_HOME/attractor/runs or ~/.attractor/runs)")
	positional, err := parseFlexible(fs, args)
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return fmt.Errorf("runs: unexpected arguments: %v (did you mean --root %s?)", positional, positional[0])
	}
	dir := *root
	if dir == "" {
		dir = defaultLogsRoot()
	}
	printRuns(os.Stdout, listRuns(dir))
	return nil
}

// printRuns formats one aligned line per run: id, graph, status,
// started-at. Fields read off disk (a run.json graph_name in particular)
// are sanitized so a crafted value cannot inject control characters or
// break the one-line-per-run layout.
func printRuns(w io.Writer, runs []runSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range runs {
		started := "-"
		if !r.StartedAt.IsZero() {
			started = r.StartedAt.Format(time.RFC3339)
		}
		graph := clean(r.Graph)
		if graph == "" {
			graph = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", clean(r.ID), graph, r.Status, started)
	}
	_ = tw.Flush()
}

// clean strips control characters (newlines, tabs, terminal escapes) from
// a field read off disk so it cannot corrupt the listing or the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
