package attractor_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/ingest"
)

func TestIngest_PersistsAndEmitsEvent(t *testing.T) {
	logsRoot := t.TempDir()
	var events []engine.Event
	srv, err := ingest.Start(logsRoot, func(ev engine.Event) {
		events = append(events, ev)
	})
	must(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	payload := map[string]any{
		"hook_name":   "post_tool",
		"run_id":      "run-1",
		"stage_id":    "implement",
		"tool":        "Edit",
		"args":        map[string]any{"file": "/tmp/x"},
		"exit_code":   0,
		"duration_ms": 12,
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("post status=%d", resp.StatusCode)
	}

	// Allow the server's emit goroutine to land.
	time.Sleep(50 * time.Millisecond)

	dir := filepath.Join(logsRoot, "implement", "tool_calls")
	entries, err := os.ReadDir(dir)
	must(t, err)
	if len(entries) != 1 {
		t.Fatalf("expected one tool_call file, got %d", len(entries))
	}
	if srv.Count() != 1 {
		t.Fatalf("server count = %d", srv.Count())
	}
	if len(events) == 0 {
		t.Fatal("expected at least one engine event from ingest")
	}
	if events[0].NodeID != "implement" || events[0].Message != "hook:post_tool" {
		t.Fatalf("event mismatch: %+v", events[0])
	}
}

func TestIngest_HealthEndpoint(t *testing.T) {
	srv, err := ingest.Start(t.TempDir(), nil)
	must(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	resp, err := http.Get(srv.URL() + "/healthz")
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/healthz status=%d", resp.StatusCode)
	}
}
