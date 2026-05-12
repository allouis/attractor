package attractor_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fabro/attractor/internal/ingest"
)

func TestToolHooks_PostHookFires(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "post.touched")
	srv, err := ingest.StartWith(ingest.Config{
		LogsRoot:    t.TempDir(),
		PostToolCmd: fmt.Sprintf("touch %q && cat > %q.body", marker, marker),
	})
	must(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	body, _ := json.Marshal(map[string]any{
		"hook_name": "post_tool",
		"stage_id":  "implement",
		"tool":      "Edit",
		"exit_code": 0,
	})
	resp, err := http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	must(t, err)
	resp.Body.Close()

	// Wait for the hook subprocess to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("post-hook did not run: %v", err)
	}
	body2, err := os.ReadFile(marker + ".body")
	must(t, err)
	if !bytes.Contains(body2, []byte(`"tool":"Edit"`)) {
		t.Fatalf("hook did not receive payload on stdin: %q", body2)
	}
}

func TestToolHooks_PreHookFires(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pre.touched")
	srv, err := ingest.StartWith(ingest.Config{
		LogsRoot:   t.TempDir(),
		PreToolCmd: fmt.Sprintf("printf 'tool=%%s\\n' \"$ATTRACTOR_HOOK_TOOL\" > %q", marker),
	})
	must(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	body, _ := json.Marshal(map[string]any{
		"hook_name": "PreToolUse", // alternate spelling — server should normalise
		"stage_id":  "implement",
		"tool":      "Bash",
	})
	resp, err := http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	must(t, err)
	resp.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, err := os.ReadFile(marker)
	must(t, err)
	if string(data) != "tool=Bash\n" {
		t.Fatalf("pre-hook env propagation: %q", data)
	}
}

func TestToolHooks_NonToolEventsBypassHooks(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "should-not-touch")
	srv, err := ingest.StartWith(ingest.Config{
		LogsRoot:    t.TempDir(),
		PreToolCmd:  fmt.Sprintf("touch %q", marker),
		PostToolCmd: fmt.Sprintf("touch %q", marker),
	})
	must(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	body, _ := json.Marshal(map[string]any{
		"hook_name": "session_start",
		"stage_id":  "x",
	})
	resp, err := http.Post(srv.URL(), "application/json", bytes.NewReader(body))
	must(t, err)
	resp.Body.Close()
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("non-tool event should not fire a tool hook")
	}
}
