package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tailMaxChunk caps one tail response so a multi-MB check log is streamed in
// bounded slices (the client re-requests from X-Next-Offset) rather than
// buffered whole into one response.
const tailMaxChunk = 1 << 20 // 1 MiB

// tailPollInterval is how often a long-poll re-checks a live stage file for
// growth while waiting.
const tailPollInterval = 150 * time.Millisecond

// getStageTail serves GET /pipelines/{id}/stages/{node}/tail?file=…&offset=N —
// an incremental read of one stage output file that long-polls while the stage
// is live (ui-run-view-v3 P5d). It serves the bytes from offset; if there are
// none and the stage is still live it waits up to s.tailWait for growth, then
// returns whatever arrived (204 when still nothing). X-Next-Offset carries the
// offset to resume from and X-Stage-Done reports whether the stage has reached a
// terminal node state (P1's event-derived derivation) so the client can stop.
//
// It serves from the daemon-visible file, whose liveness differs by runner:
//   - direct: the engine writes the stage dir in-process under logsRoot, so the
//     file grows live (P5b makes it stream).
//   - vm (post-P5c): the child's logs root is this same logsRoot shared rw, so
//     the file grows live in the guest.
//   - local: the child logs to its own temp dir, not host-visible; the file
//     appears when the per-stage phone-home upload (P3) lands at stage
//     completion, so the tail is post-stage, not mid-stage.
func (s *Server) getStageTail(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	name, ok := tailFileName(r.URL.Query().Get("file"))
	if !ok {
		http.Error(w, "file must be one of stdout, stderr, response", http.StatusBadRequest)
		return
	}
	full, ok := tailFilePath(run.logsRoot, r.PathValue("node"), name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	offset := parseOffset(r.URL.Query().Get("offset"))

	deadline := time.Now().Add(s.tailWait)
	for {
		data := readFileFrom(full, offset)
		if len(data) > 0 {
			s.writeTail(w, run, r.PathValue("node"), data, offset)
			return
		}
		// No new bytes. Stop waiting once the stage can no longer grow (its node
		// reached a terminal state, or the run itself ended) or the poll budget
		// is spent; otherwise wait a beat and re-check.
		if s.stageDone(run, r.PathValue("node")) || time.Now().After(deadline) {
			s.writeTail(w, run, r.PathValue("node"), nil, offset)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(tailPollInterval):
		}
	}
}

// writeTail sends the served bytes plus the tail control headers: X-Next-Offset
// (offset + bytes served, where the client resumes) and X-Stage-Done (the
// stage's terminal state, re-read at send time). An empty body returns 204 so a
// client distinguishes "no new content yet" from served bytes without parsing.
func (s *Server) writeTail(w http.ResponseWriter, run *Run, node string, data []byte, offset int64) {
	w.Header().Set("X-Next-Offset", strconv.FormatInt(offset+int64(len(data)), 10))
	w.Header().Set("X-Stage-Done", strconv.FormatBool(s.stageDone(run, node)))
	if len(data) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// tailFileName maps the public `file` query value to the stage filename. The
// whitelist is the containment guard for the filename half: only these three
// files are tailable, so no traversal can ride the file parameter.
func tailFileName(file string) (string, bool) {
	switch file {
	case "stdout":
		return "stdout.txt", true
	case "stderr":
		return "stderr.txt", true
	case "response":
		return "response.md", true
	}
	return "", false
}

// tailFilePath resolves the stage file within the run's logs root, confined so
// a crafted node segment cannot escape it (the getArtifact containment pattern).
// ok is false for an empty logs root or any path that escapes.
func tailFilePath(logsRoot, node, name string) (string, bool) {
	if logsRoot == "" {
		return "", false
	}
	root := filepath.Clean(logsRoot)
	full := filepath.Join(root, node, name)
	// Reject rather than neutralize: a node segment that escapes the root (via
	// "..") is a bad request, not something to silently remap inside the root.
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	// Tail the latest visit's live file (amendment A1): during a visit
	// the stream grows under {node}/v{N}; the root copy is a
	// completion-time mirror.
	return stageFilePath(filepath.Join(root, node), name), true
}

// parseOffset parses the offset query value, clamping a missing or malformed
// value to 0 (read from the start).
func parseOffset(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// readFileFrom reads up to tailMaxChunk bytes of file starting at offset. A file
// that does not exist yet (a live stage that has written nothing) or an offset
// at/after EOF yields no bytes — the caller then long-polls or returns empty.
func readFileFrom(path string, offset int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil
	}
	buf := make([]byte, tailMaxChunk)
	n, _ := f.Read(buf)
	if n <= 0 {
		return nil
	}
	return buf[:n]
}

// stageDone reports whether a stage can no longer grow: its node reached a
// terminal state in the event-derived node map (P1's derivation, reused), or
// the run itself has terminated. A done stage short-circuits the long-poll and
// tells the client to stop tailing.
func (s *Server) stageDone(run *Run, node string) bool {
	if run.isTerminal() {
		return true
	}
	_, nodes := run.deriveNodes()
	switch nodes[node].Status {
	case "completed", "failed":
		return true
	}
	return false
}
