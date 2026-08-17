package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/allouis/attractor/internal/runserver"
	"github.com/allouis/attractor/internal/runview"
)

// runSummary is one row of the `attractor runs` listing, folded from a
// run dir's on-disk state (run.json + events.jsonl). No new bookkeeping.
type runSummary struct {
	ID        string
	Graph     string
	Status    string // running | success | failed | unknown
	StartedAt time.Time
}

// listRuns folds every immediate subdirectory of root into a summary,
// most-recent-first. It reuses the run server's tolerant readers, so a
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

// summarizeRun reads one run dir. A missing or unparseable run.json
// leaves the id at the dir name, graph blank, and status "unknown"; a
// zero started-at falls back to the dir's mtime so ordering stays useful.
func summarizeRun(dir, name string) runSummary {
	m := runserver.ReadManifest(dir)
	s := runSummary{ID: name, Graph: m.GraphName, StartedAt: m.StartedAt}
	if m.RunID == "" {
		// run.json missing or malformed: best-effort row.
		s.Status = "unknown"
	} else {
		s.ID = m.RunID
		s.Status = displayStatus(runview.Document(m, runserver.ReadEvents(dir, 0)).Status)
	}
	if s.StartedAt.IsZero() {
		if fi, err := os.Stat(dir); err == nil {
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
	if _, err := parseFlexible(fs, args); err != nil {
		return err
	}
	dir := *root
	if dir == "" {
		dir = defaultLogsRoot()
	}
	printRuns(os.Stdout, listRuns(dir))
	return nil
}

// printRuns formats one aligned line per run: id, graph, status,
// started-at.
func printRuns(w io.Writer, runs []runSummary) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, r := range runs {
		started := "-"
		if !r.StartedAt.IsZero() {
			started = r.StartedAt.Format(time.RFC3339)
		}
		graph := r.Graph
		if graph == "" {
			graph = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.ID, graph, r.Status, started)
	}
	_ = tw.Flush()
}
