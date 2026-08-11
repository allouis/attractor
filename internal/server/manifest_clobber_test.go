package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/interviewer"
)

// blockHandler signals when it starts running and blocks until released, so a
// test can inspect on-disk run state while the run is mid-flight.
type blockHandler struct {
	started chan struct{}
	release chan struct{}
}

func (h blockHandler) Execute(env engine.HandlerEnv) engine.Outcome {
	close(h.started)
	<-h.release
	return engine.Outcome{Status: engine.StatusSuccess}
}

// TestManifestNotClobberedMidRun is the P5a clobber-window regression: on the
// direct (in-process) runner the daemon and the engine share one logs root, and
// before the split both wrote manifest.json — the engine's id-less schema
// clobbering the daemon's id-bearing one for the whole run window, so a daemon
// restart mid-run dropped the run from the fleet (reload skips id-less
// manifests). After the split the daemon's manifest.json keeps its id THROUGHOUT
// and the engine writes run.json instead.
func TestManifestNotClobberedMidRun(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	factory := func(iv interviewer.Interviewer) *engine.Registry {
		r := engine.NewRegistry()
		r.Register("start", handler.Start{})
		r.Register("exit", handler.Exit{})
		r.Register("block", blockHandler{started: started, release: release})
		return r
	}
	logs := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: logs, MakeHandlers: factory})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	const pipeline = `digraph d {
		start [shape=Mdiamond]
		work [type="block"]
		done [shape=Msquare]
		start -> work
		work -> done
	}`
	id, err := srv.submit(pipeline, nil, t.TempDir(), "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	manifestID := func() string {
		data, err := os.ReadFile(filepath.Join(logs, id, "manifest.json"))
		if err != nil {
			t.Fatalf("read manifest.json: %v", err)
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal manifest.json: %v", err)
		}
		return m.ID
	}

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("block node never started")
	}

	// Mid-run: the daemon's manifest.json still carries the run id.
	if got := manifestID(); got != id {
		t.Fatalf("mid-run manifest id = %q, want %q (clobbered)", got, id)
	}
	// The engine wrote its identity record to run.json, not manifest.json.
	if _, err := os.Stat(filepath.Join(logs, id, "run.json")); err != nil {
		t.Fatalf("engine run.json missing mid-run: %v", err)
	}

	close(release)
	run := waitTerminal(t, srv, id, 10*time.Second)
	if run.Status() != RunCompleted {
		t.Fatalf("status = %v, want completed", run.Status())
	}
	// Post-run: id still intact.
	if got := manifestID(); got != id {
		t.Fatalf("post-run manifest id = %q, want %q", got, id)
	}
}
