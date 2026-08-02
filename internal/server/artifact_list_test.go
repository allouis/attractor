package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestListArtifactsWalksLogsRoot drives GET /pipelines/{id}/artifacts
// (web-ui-v2-spec U4): the run's logs root is walked into a flat, sorted list
// of files with their logs-root-relative path and size. Directories are marked
// so the browser can group them; the root itself is not an entry.
func TestListArtifactsWalksLogsRoot(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot)

	write := func(rel, body string) {
		full := filepath.Join(logsRoot, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("events.jsonl", "line\n")
	write("artifacts/changes.diff", "diff --git a b\n")
	write("plan/response.md", "planned it")

	resp, err := http.Get(srv.URL() + "/pipelines/r1/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var got struct {
		Entries []struct {
			Path  string `json:"path"`
			Size  int64  `json:"size"`
			IsDir bool   `json:"is_dir"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	byPath := map[string]int64{}
	dirs := map[string]bool{}
	var order []string
	for _, e := range got.Entries {
		byPath[e.Path] = e.Size
		dirs[e.Path] = e.IsDir
		order = append(order, e.Path)
	}

	// Every file surfaces at its logs-root-relative path.
	for _, want := range []string{"events.jsonl", "artifacts/changes.diff", "plan/response.md"} {
		if _, ok := byPath[want]; !ok {
			t.Errorf("missing entry %q; got %v", want, order)
		}
	}
	// The root itself is never an entry.
	if _, ok := byPath[""]; ok {
		t.Error("root should not appear as an entry")
	}
	if _, ok := byPath["."]; ok {
		t.Error(`"." should not appear as an entry`)
	}
	// A directory is flagged; a file is not, with its real size.
	if !dirs["artifacts"] {
		t.Error("artifacts should be flagged is_dir")
	}
	if dirs["artifacts/changes.diff"] {
		t.Error("changes.diff should not be flagged is_dir")
	}
	if byPath["artifacts/changes.diff"] != int64(len("diff --git a b\n")) {
		t.Errorf("changes.diff size = %d, want %d", byPath["artifacts/changes.diff"], len("diff --git a b\n"))
	}
	// Sorted order is deterministic.
	for i := 1; i < len(order); i++ {
		if order[i-1] > order[i] {
			t.Errorf("entries not sorted: %q before %q", order[i-1], order[i])
		}
	}
}

// TestListArtifactsUnknownRun 404s rather than leaking a listing for a run the
// registry does not know.
func TestListArtifactsUnknownRun(t *testing.T) {
	srv, _ := newStageTestServer(t)
	resp, err := http.Get(srv.URL() + "/pipelines/nope/artifacts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
