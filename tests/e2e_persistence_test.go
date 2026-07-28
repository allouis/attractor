package attractor_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

const persistenceDOT = `digraph p {
	start [shape=Mdiamond]
	work [prompt="x"]
	done [shape=Msquare]
	start -> work -> done
}`

func TestPersistence_ManifestAndSourceWrittenOnSubmit(t *testing.T) {
	logsRoot := t.TempDir()
	srv := server.New(server.Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     logsRoot,
		MakeHandlers: server.DefaultHandlers(handler.Codergen{}),
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })

	id := submitPipeline(t, srv, persistenceDOT)
	waitForRunStatus(t, srv, id, "completed")

	// manifest.json + source.dot + events.jsonl all present.
	for _, f := range []string{"manifest.json", "source.dot", "events.jsonl"} {
		path := filepath.Join(logsRoot, id, f)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	// manifest reflects completion.
	data, err := os.ReadFile(filepath.Join(logsRoot, id, "manifest.json"))
	must(t, err)
	var m server.Manifest
	must(t, json.Unmarshal(data, &m))
	if m.ID != id {
		t.Fatalf("manifest id = %q, want %q", m.ID, id)
	}
	if m.Status != server.RunCompleted {
		t.Fatalf("manifest status = %q", m.Status)
	}
	// events.jsonl has multiple JSON lines.
	ev, err := os.ReadFile(filepath.Join(logsRoot, id, "events.jsonl"))
	must(t, err)
	if strings.Count(string(ev), "\n") < 3 {
		t.Fatalf("expected several event lines, got %q", ev)
	}
}

func TestPersistence_RegistryRebuildsAfterRestart(t *testing.T) {
	logsRoot := t.TempDir()

	// First server: run a pipeline to completion.
	srv1 := server.New(server.Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     logsRoot,
		MakeHandlers: server.DefaultHandlers(handler.Codergen{}),
	})
	must(t, srv1.Start())
	id := submitPipeline(t, srv1, persistenceDOT)
	waitForRunStatus(t, srv1, id, "completed")
	must(t, srv1.Close())

	// Second server pointed at the same logs root should see the run.
	srv2 := server.New(server.Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     logsRoot,
		MakeHandlers: server.DefaultHandlers(handler.Codergen{}),
	})
	must(t, srv2.Start())
	t.Cleanup(func() { _ = srv2.Close() })

	resp, err := http.Get(srv2.URL() + "/pipelines/" + id)
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("rebuild: GET /pipelines/%s = %d %s", id, resp.StatusCode, body)
	}
	var summary struct {
		Status string `json:"status"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&summary))
	if summary.Status != "completed" {
		t.Fatalf("rebuilt run status = %q, want completed", summary.Status)
	}

	// Checkpoint should still be reachable.
	cp, err := http.Get(srv2.URL() + "/pipelines/" + id + "/checkpoint")
	must(t, err)
	defer cp.Body.Close()
	if cp.StatusCode != http.StatusOK {
		t.Fatalf("checkpoint after restart status=%d", cp.StatusCode)
	}
}

func TestPersistence_InterruptedRunMarkedCancelled(t *testing.T) {
	logsRoot := t.TempDir()
	runID := "interrupted-run"
	dir := filepath.Join(logsRoot, runID)
	must(t, os.MkdirAll(dir, 0o755))
	// Simulate a server that crashed mid-run.
	m := server.Manifest{
		ID:       runID,
		Status:   server.RunRunning,
		LogsRoot: dir,
	}
	data, _ := json.Marshal(m)
	must(t, os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644))

	srv := server.New(server.Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     logsRoot,
		MakeHandlers: server.DefaultHandlers(handler.Codergen{}),
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })

	resp, err := http.Get(srv.URL() + "/pipelines/" + runID)
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var summary struct {
		Status string `json:"status"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&summary))
	if summary.Status != "cancelled" {
		t.Fatalf("interrupted run should be cancelled, got %q", summary.Status)
	}
}
