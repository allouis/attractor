package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/interviewer"
	"github.com/allouis/attractor/internal/items"
	"github.com/allouis/attractor/internal/items/source"
)

// postRestart POSTs /pipelines/{id}/restart and returns the response.
func postRestart(t *testing.T, url, id string) *http.Response {
	t.Helper()
	resp, err := http.Post(url+"/pipelines/"+id+"/restart", "application/json", bytes.NewReader([]byte(`{}`)))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// pollRunUntil polls GET /pipelines/{id} until status == want (or a deadline).
func pollRunUntil(t *testing.T, url, id string, want RunStatus) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get(url + "/pipelines/" + id)
		if err != nil {
			t.Fatal(err)
		}
		var summary map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&summary)
		resp.Body.Close()
		if status, _ := summary["status"].(string); status == string(want) {
			return summary
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s never reached %q", id, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// countLines returns the number of newline-terminated lines in a file, or 0
// when it is absent.
func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return bytes.Count(data, []byte("\n"))
}

// TestRestart_ResumesFromFailedNode proves POST /pipelines/{id}/restart re-runs
// a failed run from its checkpoint (web-ui-v2-spec U6, re-run-from-failure):
// node `a` (already completed) is NOT re-executed on resume, and node `b`
// (fail-once) re-executes and now succeeds, driving the run to completion.
func TestRestart_ResumesFromFailedNode(t *testing.T) {
	catalog := t.TempDir()
	repoDir := t.TempDir()
	scratch := t.TempDir()
	counter := filepath.Join(scratch, "counter")
	sentinel := filepath.Join(scratch, "sentinel")
	dir := filepath.Join(catalog, "resume")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `digraph resume {
		vars="repo"
		start [shape=Mdiamond]
		a [type="tool", tool_command="echo x >> ` + counter + `"]
		b [type="tool", tool_command="[ -f ` + sentinel + ` ] || { touch ` + sentinel + `; exit 1; }"]
		done [shape=Msquare]
		start -> a -> b -> done
	}`
	if err := os.WriteFile(filepath.Join(dir, "pipeline.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := runFormServer(t, catalog, nil, items.Repos{"allouis/attractor": repoDir})

	_, id := postRunWorkflow(t, srv.URL(), "resume", map[string]any{"repo": "allouis/attractor"})
	if got := pollRunSummary(t, srv.URL(), id)["status"]; got != string(RunFailed) {
		t.Fatalf("first run status = %v, want failed", got)
	}
	if n := countLines(t, counter); n != 1 {
		t.Fatalf("counter = %d lines after first run, want 1", n)
	}

	resp := postRestart(t, srv.URL(), id)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("restart status = %d, want 202", resp.StatusCode)
	}
	pollRunUntil(t, srv.URL(), id, RunCompleted)
	if n := countLines(t, counter); n != 1 {
		t.Errorf("counter = %d lines after restart, want 1 (node a must not re-run)", n)
	}
}

// TestRestart_RejectsNonFailedRun proves restart only applies to a failed run:
// a completed run has nothing to resume, so restart is a 409.
func TestRestart_RejectsNonFailedRun(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "implement", "repo")
	srv := runFormServer(t, catalog, nil, items.Repos{"allouis/attractor": t.TempDir()})
	_, id := postRunWorkflow(t, srv.URL(), "implement", map[string]any{"repo": "allouis/attractor"})
	if got := pollRunSummary(t, srv.URL(), id)["status"]; got != string(RunCompleted) {
		t.Fatalf("run status = %v, want completed", got)
	}
	resp := postRestart(t, srv.URL(), id)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("restart of completed run = %d, want 409", resp.StatusCode)
	}
}

// TestRestart_UnknownRun404 guards the miss path.
func TestRestart_UnknownRun404(t *testing.T) {
	srv := runFormServer(t, t.TempDir(), nil, items.Repos{})
	resp := postRestart(t, srv.URL(), "nope")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("restart of unknown run = %d, want 404", resp.StatusCode)
	}
}

// registerRun injects a run directly into the registry for tests that need a
// run in a state the normal admission path won't produce (a reloaded shell, a
// non-direct placement).
func registerRun(t *testing.T, srv *Server, run *Run) {
	t.Helper()
	run.logsRoot = t.TempDir()
	run.subscribers = map[chan engine.Event]struct{}{}
	run.questions = map[string]*pendingQuestion{}
	srv.registry.mu.Lock()
	srv.registry.runs[run.ID] = run
	srv.registry.mu.Unlock()
}

// TestRunSummary_ResumableBit proves the summary flags whether a failed run can
// resume from its checkpoint (web-ui-v2-spec U6): only a direct-launcher run
// with its engine deps still in memory (never a reloaded shell, never a
// local/vm run whose checkpoint died with the child). The UI gates the
// re-run-from-failure button on this bit.
func TestRunSummary_ResumableBit(t *testing.T) {
	stub := HandlerFactory(func(interviewer.Interviewer) *engine.Registry { return nil })
	cases := []struct {
		name string
		run  *Run
		want bool
	}{
		{"direct + relaunchable", &Run{placement: "direct", prepared: &engine.PreparedGraph{}, factory: stub}, true},
		{"default (empty) placement", &Run{placement: "", prepared: &engine.PreparedGraph{}, factory: stub}, true},
		{"reloaded shell (nil deps)", &Run{placement: "direct"}, false},
		{"vm placement", &Run{placement: "vm", prepared: &engine.PreparedGraph{}, factory: stub}, false},
	}
	for _, c := range cases {
		got, _ := c.run.Summary()["resumable"].(bool)
		if got != c.want {
			t.Errorf("%s: resumable = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestRestart_RejectsUnresumableRun proves the restart handler refuses a run it
// cannot resume rather than enqueuing it into a crash (web-ui-v2-spec U6): a
// reloaded shell (nil prepared/factory → nil-func panic in execute) and a
// local/vm run (checkpoint gone with the child → silent full re-run) both 409.
func TestRestart_RejectsUnresumableRun(t *testing.T) {
	srv := runFormServer(t, t.TempDir(), nil, items.Repos{})
	stub := HandlerFactory(func(interviewer.Interviewer) *engine.Registry { return nil })
	cases := []struct {
		name string
		run  *Run
	}{
		{"reloaded", &Run{ID: "reloaded", status: RunFailed, placement: "direct"}},
		{"vm", &Run{ID: "vm-run", status: RunFailed, placement: "vm", prepared: &engine.PreparedGraph{}, factory: stub}},
	}
	for _, c := range cases {
		registerRun(t, srv, c.run)
		resp := postRestart(t, srv.URL(), c.run.ID)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("%s: restart = %d, want 409", c.name, resp.StatusCode)
		}
	}
}

// TestRunSummary_ExposesLaunchVars proves the run summary carries the declared
// vars a manual re-run resubmits (web-ui-v2-spec U6): the run's launch vars,
// with the seeded item.* metadata and static check.* commands filtered out so
// the Run modal prefills only what the human filled in.
func TestRunSummary_ExposesLaunchVars(t *testing.T) {
	catalog := t.TempDir()
	writeVarsWorkflow(t, catalog, "review-pr", "repo,pr_number,title")
	repoDir := t.TempDir()
	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	fs := &fakeSource{getItem: source.Item{
		Ref:   ref,
		Title: "Fix login",
		Vars: map[string]string{
			"repo":      "allouis/attractor",
			"pr_number": "42",
			"title":     "old title",
		},
	}}
	srv := runFormServer(t, catalog, map[string]source.Source{"github": fs}, items.Repos{"allouis/attractor": repoDir})

	_, id := postRunWorkflow(t, srv.URL(), "review-pr", map[string]any{
		"item_ref": ref,
		"vars":     map[string]string{"title": "new title"},
	})
	run, ok := srv.registry.Get(id)
	if !ok {
		t.Fatalf("run %q not registered", id)
	}
	vars, ok := run.Summary()["vars"].(map[string]string)
	if !ok {
		t.Fatalf("summary vars = %v, want a map[string]string", run.Summary()["vars"])
	}
	want := map[string]string{"repo": "allouis/attractor", "pr_number": "42", "title": "new title"}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("summary vars[%q] = %q, want %q", k, vars[k], v)
		}
	}
	for k := range vars {
		if k == "" || len(k) >= 5 && (k[:5] == "item." || k[:5] == "check") {
			t.Errorf("summary vars leaked seeded key %q", k)
		}
	}
	pollRunSummary(t, srv.URL(), id) // let the run finish before tempdir cleanup
}

// TestRestart_RotatesEventLog proves a resumed run's durable event log is not
// corrupted by the old failed attempt (web-ui-v2-spec U6): after restart, the
// on-disk events.jsonl carries no stale pipeline_failed and no duplicate seqs,
// so a reboot (empty in-memory history → replay from disk) shows the resumed
// run as completed, not failed. Reproduces the reboot path by clearing history.
func TestRestart_RotatesEventLog(t *testing.T) {
	catalog := t.TempDir()
	repoDir := t.TempDir()
	scratch := t.TempDir()
	sentinel := filepath.Join(scratch, "sentinel")
	dir := filepath.Join(catalog, "resume")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `digraph resume {
		vars="repo"
		start [shape=Mdiamond]
		a [type="tool", tool_command="true"]
		b [type="tool", tool_command="[ -f ` + sentinel + ` ] || { touch ` + sentinel + `; exit 1; }"]
		done [shape=Msquare]
		start -> a -> b -> done
	}`
	if err := os.WriteFile(filepath.Join(dir, "pipeline.dot"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := runFormServer(t, catalog, nil, items.Repos{"allouis/attractor": repoDir})

	_, id := postRunWorkflow(t, srv.URL(), "resume", map[string]any{"repo": "allouis/attractor"})
	pollRunSummary(t, srv.URL(), id)
	resp := postRestart(t, srv.URL(), id)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("restart status = %d, want 202", resp.StatusCode)
	}
	pollRunUntil(t, srv.URL(), id, RunCompleted)

	// Reboot simulation: drop in-memory history so a fresh Subscribe replays
	// the run purely from the on-disk events.jsonl.
	run, _ := srv.registry.Get(id)
	run.mu.Lock()
	run.history = nil
	run.mu.Unlock()

	seen := map[int64]bool{}
	var terminal engine.EventKind
	for ev := range run.Subscribe(0) {
		if ev.Kind == engine.EventPipelineFailed {
			t.Errorf("resumed run's disk log still carries a stale pipeline_failed")
		}
		if ev.Seq != 0 && seen[ev.Seq] {
			t.Errorf("duplicate event seq %d in resumed run's disk log", ev.Seq)
		}
		seen[ev.Seq] = true
		if ev.Kind == engine.EventPipelineCompleted || ev.Kind == engine.EventPipelineFailed {
			terminal = ev.Kind
		}
	}
	if terminal != engine.EventPipelineCompleted {
		t.Errorf("replayed terminal event = %q, want pipeline_completed", terminal)
	}
}
