package server

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tailURL(base, id, node, file string, offset int64) string {
	return fmt.Sprintf("%s/pipelines/%s/stages/%s/tail?file=%s&offset=%d", base, id, node, file, offset)
}

// writeStageFile creates logsRoot/<node>/<name> with body.
func writeStageFile(t *testing.T, logsRoot, node, name, body string) string {
	t.Helper()
	dir := filepath.Join(logsRoot, node)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func setRunStatus(srv *Server, id string, status RunStatus) {
	run, _ := srv.registry.Get(id)
	run.mu.Lock()
	run.status = status
	run.mu.Unlock()
}

// TestStageTailOffsetRead serves the bytes from the requested offset and
// reports the next offset to resume from.
func TestStageTailOffsetRead(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot)
	setRunStatus(srv, "r1", RunCompleted) // done → no long-poll
	writeStageFile(t, logsRoot, "work", "stdout.txt", "hello world")

	resp, err := http.Get(tailURL(srv.URL(), "r1", "work", "stdout", 6))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "world" {
		t.Fatalf("body = %q, want %q", body, "world")
	}
	if got := resp.Header.Get("X-Next-Offset"); got != "11" {
		t.Fatalf("X-Next-Offset = %q, want 11", got)
	}
	if got := resp.Header.Get("X-Stage-Done"); got != "true" {
		t.Fatalf("X-Stage-Done = %q, want true", got)
	}
}

// TestStageTailLongPollGrowth blocks on a live stage until the file grows, then
// returns the appended bytes.
func TestStageTailLongPollGrowth(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	srv.tailWait = 3 * time.Second
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot) // status "" → live
	p := writeStageFile(t, logsRoot, "work", "stdout.txt", "first\n")

	go func() {
		time.Sleep(150 * time.Millisecond)
		f, _ := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0o644)
		_, _ = f.WriteString("second\n")
		_ = f.Close()
	}()

	start := time.Now()
	resp, err := http.Get(tailURL(srv.URL(), "r1", "work", "stdout", 6)) // offset past "first\n"
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "second\n" {
		t.Fatalf("body = %q, want %q", body, "second\n")
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("returned too early (%s) — did not long-poll", time.Since(start))
	}
	if got := resp.Header.Get("X-Stage-Done"); got != "false" {
		t.Fatalf("X-Stage-Done = %q, want false (stage live)", got)
	}
	if got := resp.Header.Get("X-Next-Offset"); got != "13" {
		t.Fatalf("X-Next-Offset = %q, want 13", got)
	}
}

// TestStageTailEmptyWhenLiveNoGrowth returns 204 with the same offset when a
// live stage produced no new bytes within the wait window.
func TestStageTailEmptyWhenLiveNoGrowth(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	srv.tailWait = 200 * time.Millisecond
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot) // live
	writeStageFile(t, logsRoot, "work", "stdout.txt", "abc")

	resp, err := http.Get(tailURL(srv.URL(), "r1", "work", "stdout", 3)) // at EOF
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Next-Offset"); got != "3" {
		t.Fatalf("X-Next-Offset = %q, want 3 (unchanged)", got)
	}
	if got := resp.Header.Get("X-Stage-Done"); got != "false" {
		t.Fatalf("X-Stage-Done = %q, want false", got)
	}
}

// TestStageTailDoneFlagFlips: once the run is terminal, a no-growth read returns
// immediately with X-Stage-Done true (no long-poll).
func TestStageTailDoneFlagFlips(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	srv.tailWait = 10 * time.Second // long; must NOT be hit
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot)
	writeStageFile(t, logsRoot, "work", "stdout.txt", "abc")
	setRunStatus(srv, "r1", RunCompleted)

	start := time.Now()
	resp, err := http.Get(tailURL(srv.URL(), "r1", "work", "stdout", 3))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if time.Since(start) > 2*time.Second {
		t.Fatalf("blocked %s on a done stage — should return immediately", time.Since(start))
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Stage-Done"); got != "true" {
		t.Fatalf("X-Stage-Done = %q, want true", got)
	}
}

// TestStageTailFileMissingIsLivePoll: a live stage whose file does not exist yet
// long-polls and returns empty (not 404) — the file appears once the stage
// writes or uploads it.
func TestStageTailFileMissingIsLivePoll(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	srv.tailWait = 150 * time.Millisecond
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot)
	setRunStatus(srv, "r1", RunCompleted) // terminal → returns at once

	resp, err := http.Get(tailURL(srv.URL(), "r1", "work", "response", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a missing file", resp.StatusCode)
	}
}

// TestStageTailUnknownRun 404s.
func TestStageTailUnknownRun(t *testing.T) {
	srv, _ := newStageTestServer(t)
	resp, err := http.Get(tailURL(srv.URL(), "nope", "work", "stdout", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestStageTailBadFileParam 400s for a file outside the whitelist.
func TestStageTailBadFileParam(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	logsRoot := filepath.Join(tmp, "r1")
	addRun(srv, "r1", logsRoot)
	resp, err := http.Get(tailURL(srv.URL(), "r1", "work", "secret", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestTailFilePathRejectsTraversal: a node segment that escapes the logs root is
// refused (the file whitelist covers the filename half).
func TestTailFilePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, node := range []string{"../evil", "../../etc", "..", "a/../../b"} {
		if _, ok := tailFilePath(root, node, "stdout.txt"); ok {
			t.Errorf("node %q escaped the logs root but was accepted", node)
		}
	}
	if _, ok := tailFilePath(root, "work", "stdout.txt"); !ok {
		t.Error("legitimate node rejected")
	}
	if _, ok := tailFilePath("", "work", "stdout.txt"); ok {
		t.Error("empty logs root accepted")
	}
}
