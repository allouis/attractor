package server

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/config"
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
// overriding the daemon default; an empty field falls back to the default. An
// unknown runner is a submit rejection (VM3), not a silent fallback.
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
	id1, _ := srv.submit("digraph{ s [shape=Mdiamond]; e [shape=Msquare]; s -> e }", nil, tmp, "", "", "", "", "vm", "")
	waitTerminal(t, srv, id1, 5*time.Second)
	// no runner → default
	id2, _ := srv.submit("digraph{ s [shape=Mdiamond]; e [shape=Msquare]; s -> e }", nil, tmp, "", "", "", "", "", "")
	waitTerminal(t, srv, id2, 5*time.Second)
	// unknown runner → submit rejection (VM3), nothing enqueued
	if _, err := srv.submit("digraph{ s [shape=Mdiamond]; e [shape=Msquare]; s -> e }", nil, tmp, "", "", "", "", "bogus", ""); err == nil {
		t.Fatal("expected unknown-runner rejection")
	}

	if !slices.Equal(seen, []string{"vm", "default"}) {
		t.Fatalf("launcher order = %v, want [vm default]", seen)
	}
}

// A repo's declared runner+image apply to a dispatch that names neither —
// the daily driver "this repo → this VM" (VM3). The run's cwd is the repo's
// registered checkout, so resolution keys off the repo identity.
func TestSubmitResolvesRepoDeclaredPlacement(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repoDir := t.TempDir()
	doc := config.Document{Repos: map[string]config.RepoConfig{
		"a/b": {Path: repoDir, Runner: "vm", VM: &config.VMConfig{Image: "node-ts"}},
	}}
	mustNil(t, doc.Save(home))

	var got string
	rec := imageRecordingLauncher{got: &got}
	srv := New(Config{
		Addr: "127.0.0.1:0", LogsRoot: t.TempDir(),
		Launcher:  NewDirectLauncher(),
		Launchers: map[string]Launcher{"vm": rec, "direct": NewDirectLauncher()},
	})
	mustNil(t, srv.Start())
	defer srv.Close()

	id, err := srv.submit(doneGraphSrv, nil, repoDir, "", "", "", "", "", "")
	mustNil(t, err)
	waitTerminal(t, srv, id, 5*time.Second)

	run, ok := srv.registry.Get(id)
	if !ok {
		t.Fatal("run not found")
	}
	if run.placement != "vm" {
		t.Errorf("placement = %q, want vm (from repo config)", run.placement)
	}
	if got != "node-ts" {
		t.Errorf("image at launch = %q, want node-ts (from repo config)", got)
	}
}

// A submission naming a vm image that is not registered is rejected at submit
// (VM3), listing the registered names — not deferred to a boot failure.
func TestSubmitRejectsUnknownImage(t *testing.T) {
	tmp := t.TempDir()
	vm := NewVMLauncherWithImages(map[string]string{"default": "/a", "node-ts": "/b"}, "default", t.TempDir())
	srv := New(Config{
		Addr: "127.0.0.1:0", LogsRoot: tmp,
		Launcher:  NewDirectLauncher(),
		Launchers: map[string]Launcher{"vm": vm, "direct": NewDirectLauncher()},
	})
	_, err := srv.submit(doneGraphSrv, nil, tmp, "", "", "", "", "vm", "bogus")
	if err == nil {
		t.Fatal("expected submit rejection for unknown image")
	}
	if !strings.Contains(err.Error(), "node-ts") {
		t.Errorf("error should list registered images: %v", err)
	}
}

// imageRecordingLauncher captures the run's requested VM image name at
// launch, then self-completes like a phone-home child.
type imageRecordingLauncher struct{ got *string }

func (l imageRecordingLauncher) Launch(run *Run, _ string) error {
	*l.got = run.image
	run.Ingest(engine.Event{Kind: engine.EventPipelineCompleted, Status: "success"})
	return nil
}

// A submission's `image` field is carried onto the run so the vm launcher
// resolves the right boot script per run (VM1) — the symmetric partner of
// the `runner` override. Repo-config precedence and submit-time rejection of
// unknown names are VM3.
func TestPerRunImageCarriedToLauncher(t *testing.T) {
	tmp := t.TempDir()
	var got string
	rec := imageRecordingLauncher{got: &got}
	srv := New(Config{
		Addr: "127.0.0.1:0", LogsRoot: tmp,
		Launcher:  NewDirectLauncher(),
		Launchers: map[string]Launcher{"vm": rec, "direct": NewDirectLauncher()},
	})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	id, err := srv.submit("digraph{ s [shape=Mdiamond]; e [shape=Msquare]; s -> e }", nil, tmp, "", "", "", "", "vm", "python")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitTerminal(t, srv, id, 5*time.Second)
	if got != "python" {
		t.Fatalf("run.image at launch = %q, want python", got)
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
