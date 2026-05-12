// Package ingest is the localhost HTTP endpoint that the Claude Code
// hookshim binary posts to during a pipeline run. Hook payloads land
// here and are persisted to `{logs_root}/{node_id}/tool_calls/<id>.json`
// plus surfaced as engine events via the supplied Emit callback.
package ingest

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fabro/attractor/internal/engine"
)

// Server is a small localhost HTTP server. The Attractor engine starts
// one per run and tears it down on completion.
type Server struct {
	addr     string
	listener net.Listener
	srv      *http.Server
	logsRoot string

	mu     sync.RWMutex
	emit   func(engine.Event)
	closed atomic.Bool
	count  atomic.Int64
}

// Start binds an ephemeral TCP port on 127.0.0.1 and begins serving.
// The returned Server exposes the URL the shim should post to via URL().
func Start(logsRoot string, emit func(engine.Event)) (*Server, error) {
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("ingest: listen: %w", err)
	}
	s := &Server{
		addr:     ln.Addr().String(),
		listener: ln,
		logsRoot: logsRoot,
		emit:     emit,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handlePost)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	s.srv = &http.Server{
		Handler:           mux,
		ReadTimeout:       2 * time.Second,
		ReadHeaderTimeout: 1 * time.Second,
		WriteTimeout:      2 * time.Second,
	}
	go s.srv.Serve(ln)
	return s, nil
}

// URL returns the base URL hookshims should post to.
func (s *Server) URL() string { return "http://" + s.addr }

// Close stops the HTTP server. Safe to call multiple times.
func (s *Server) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	ctx, cancel := contextWithTimeout(time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// Count returns the total number of hook events ingested. Useful for
// test assertions.
func (s *Server) Count() int64 { return s.count.Load() }

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.Body.Close()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.count.Add(1)
	// Persist to {logs_root}/{stage_id}/tool_calls/<n>.json
	stageID, _ := payload["stage_id"].(string)
	hookName, _ := payload["hook_name"].(string)
	if stageID != "" {
		dir := filepath.Join(s.logsRoot, stageID, "tool_calls")
		if err := os.MkdirAll(dir, 0o755); err == nil {
			path := filepath.Join(dir, fmt.Sprintf("%d-%s.json", s.count.Load(), safeName(hookName)))
			_ = os.WriteFile(path, body, 0o644)
		}
	}
	s.mu.RLock()
	emit := s.emit
	s.mu.RUnlock()
	if emit != nil {
		emit(engine.Event{
			Kind:    engine.EventStageProgress,
			NodeID:  stageID,
			Message: fmt.Sprintf("hook:%s", hookName),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func safeName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "hook"
	}
	return string(out)
}
