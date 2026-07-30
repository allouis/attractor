package server

import (
	"slices"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// The default launcher is `direct` (in-process) so existing behavior is
// unchanged (decision D5).
func TestDefaultLauncherIsDirect(t *testing.T) {
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	if _, ok := srv.launcher.(directLauncher); !ok {
		t.Fatalf("default launcher = %T, want directLauncher", srv.launcher)
	}
}

// A phone-home run self-completes from its ingested terminal event: the
// daemon does not drive it in-process, so Ingest of pipeline_completed
// must transition the run to RunCompleted and set its outcome.
func TestIngestTerminalEventCompletesRun(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)

	run.Ingest(engine.Event{Kind: engine.EventStageStarted, NodeID: "plan"})
	if run.Status() == RunCompleted {
		t.Fatalf("completed before terminal event")
	}
	run.Ingest(engine.Event{Kind: engine.EventPipelineCompleted, Status: "success"})

	if got := run.Status(); got != RunCompleted {
		t.Fatalf("status = %v, want completed", got)
	}
	if run.outcome == nil || run.outcome.Status != engine.StatusSuccess {
		t.Fatalf("outcome = %+v, want success", run.outcome)
	}
	if run.completedAt.IsZero() {
		t.Fatalf("completedAt not set")
	}
}

// recordingLauncher notes it ran and immediately completes the run (as a
// phone-home child would, via the terminal event).
type recordingLauncher struct {
	name string
	seen *[]string
}

func (l recordingLauncher) Launch(run *Run, _ string) error {
	*l.seen = append(*l.seen, l.name)
	run.Ingest(engine.Event{Kind: engine.EventPipelineCompleted, Status: "success"})
	return nil
}

// A submission's `runner` field selects a named launcher per run (V18),
// overriding the daemon default; unknown/empty falls back to the default.
func TestPerRunPlacementSelectsNamedLauncher(t *testing.T) {
	tmp := t.TempDir()
	var seen []string
	def := recordingLauncher{name: "default", seen: &seen}
	vm := recordingLauncher{name: "vm", seen: &seen}
	srv := New(Config{
		Addr: "127.0.0.1:0", LogsRoot: tmp,
		Launcher:  def,
		Launchers: map[string]Launcher{"vm": vm, "direct": def},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	// runner="vm" → vm launcher
	id1, _ := srv.submit("digraph{ s [shape=Mdiamond]; e [shape=Msquare]; s -> e }", nil, tmp, "", "", "", "vm")
	waitTerminal(t, srv, id1, 5*time.Second)
	// no runner → default
	id2, _ := srv.submit("digraph{ s [shape=Mdiamond]; e [shape=Msquare]; s -> e }", nil, tmp, "", "", "", "")
	waitTerminal(t, srv, id2, 5*time.Second)
	// unknown runner → default
	id3, _ := srv.submit("digraph{ s [shape=Mdiamond]; e [shape=Msquare]; s -> e }", nil, tmp, "", "", "", "bogus")
	waitTerminal(t, srv, id3, 5*time.Second)

	if !slices.Equal(seen, []string{"vm", "default", "default"}) {
		t.Fatalf("launcher order = %v, want [vm default default]", seen)
	}
}

// A phone-home run transitions queued→running when its child reports
// pipeline_started (nothing drives it in-process to flip the status).
func TestIngestPipelineStartedMarksRunning(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)
	if run.Status() != RunQueued {
		t.Fatalf("initial status = %v, want queued", run.Status())
	}
	run.Ingest(engine.Event{Kind: engine.EventPipelineStarted, NodeID: "start"})
	if run.Status() != RunRunning {
		t.Fatalf("status = %v, want running", run.Status())
	}
}

// reportURL derives a loopback base URL a local child can reach even when
// the daemon binds a wildcard host.
func TestReportURLLoopbackForWildcard(t *testing.T) {
	srv := New(Config{Addr: "0.0.0.0:7681", LogsRoot: t.TempDir()})
	if got := srv.reportURL(); got != "http://127.0.0.1:7681" {
		t.Fatalf("reportURL = %q", got)
	}
}

func TestIngestTerminalEventClosesSubscribers(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", nil)
	run.mu.Lock()
	run.status = RunRunning
	run.mu.Unlock()
	ch := run.Subscribe(0)
	run.Ingest(engine.Event{Kind: engine.EventPipelineFailed, Message: "boom"})
	select {
	case _, open := <-drainUntilClose(ch):
		_ = open
	case <-time.After(time.Second):
		t.Fatal("subscriber not closed after terminal event")
	}
}

// drainUntilClose reads ch until it closes, returning a channel that
// yields once closed.
func drainUntilClose(ch chan engine.Event) chan bool {
	out := make(chan bool)
	go func() {
		for range ch {
		}
		out <- false
		close(out)
	}()
	return out
}
