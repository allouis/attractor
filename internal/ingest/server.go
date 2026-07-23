// Package ingest is the localhost HTTP endpoint that the Claude Code
// hookshim binary posts to during a pipeline run. Hook payloads land
// here and are persisted to `{logs_root}/{node_id}/tool_calls/<id>.json`
// plus surfaced as engine events via the supplied Emit callback.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	mu          sync.RWMutex
	emit        func(engine.Event)
	preToolCmd  string
	postToolCmd string
	closed      atomic.Bool
	count       atomic.Int64
}

// Config configures a Server. Empty fields disable optional features.
type Config struct {
	LogsRoot string
	Emit     func(engine.Event)
	// PreToolCmd is the shell command to run when a `pre_tool` /
	// `PreToolUse` hook payload arrives. Non-empty triggers tool_hooks
	// support per spec §9.7. The command runs with ATTRACTOR_HOOK_*
	// environment variables describing the call.
	PreToolCmd string
	// PostToolCmd is the analogous shell command for post-tool hooks.
	PostToolCmd string
}

// Start binds an ephemeral TCP port on 127.0.0.1 and begins serving
// with default config (no tool hooks).
func Start(logsRoot string, emit func(engine.Event)) (*Server, error) {
	return StartWith(Config{LogsRoot: logsRoot, Emit: emit})
}

// StartWith binds an ephemeral TCP port on 127.0.0.1 and begins serving
// using the supplied configuration.
func StartWith(cfg Config) (*Server, error) {
	if err := os.MkdirAll(cfg.LogsRoot, 0o755); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("ingest: listen: %w", err)
	}
	s := &Server{
		addr:        ln.Addr().String(),
		listener:    ln,
		logsRoot:    cfg.LogsRoot,
		emit:        cfg.Emit,
		preToolCmd:  cfg.PreToolCmd,
		postToolCmd: cfg.PostToolCmd,
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
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
	preCmd := s.preToolCmd
	postCmd := s.postToolCmd
	s.mu.RUnlock()
	if emit != nil {
		emit(engine.Event{
			Kind:    engine.EventStageProgress,
			NodeID:  stageID,
			Message: fmt.Sprintf("hook:%s", hookName),
		})
	}
	switch normalizeHookName(hookName) {
	case "pre_tool":
		if preCmd != "" {
			runToolHook(preCmd, payload, stageID, hookName)
		}
	case "post_tool":
		if postCmd != "" {
			runToolHook(postCmd, payload, stageID, hookName)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// normalizeHookName maps the various spellings Claude Code uses
// (PreToolUse, PostToolUse, pre_tool, post_tool, etc.) to a small
// canonical set.
func normalizeHookName(name string) string {
	lower := strings.ToLower(name)
	switch lower {
	case "pretooluse", "pre_tool", "pre_tool_use":
		return "pre_tool"
	case "posttooluse", "post_tool", "post_tool_use":
		return "post_tool"
	}
	return lower
}

// runToolHook executes the configured shell command synchronously,
// piping the payload as JSON on stdin and exposing key fields via
// environment variables. Errors are best-effort: pre-hook block
// semantics (spec §9.7) would require a response back to Claude Code,
// which the current hookshim does not yet propagate — block decisions
// are logged but not enforced.
func runToolHook(cmd string, payload map[string]any, stageID, hookName string) {
	tool := stringOf(payload["tool"])
	toolName := stringOf(payload["tool_name"])
	if tool == "" {
		tool = toolName
	}
	exitCode := stringOf(payload["exit_code"])
	body, _ := json.Marshal(payload)
	exe := exec.Command("/bin/sh", "-c", cmd)
	exe.Stdin = strings.NewReader(string(body))
	exe.Env = append(os.Environ(),
		"ATTRACTOR_HOOK_NAME="+hookName,
		"ATTRACTOR_HOOK_STAGE="+stageID,
		"ATTRACTOR_HOOK_TOOL="+tool,
		"ATTRACTOR_HOOK_EXIT="+exitCode,
	)
	// Fire-and-forget but with a 2s deadline so a stuck hook can't pin
	// the ingest goroutine.
	done := make(chan struct{})
	go func() {
		_ = exe.Run()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = exe.Process.Kill()
	}
}

func stringOf(v any) string {
	if v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case float64:
		return fmt.Sprintf("%v", s)
	case int, int64:
		return fmt.Sprintf("%d", s)
	case bool:
		if s {
			return "true"
		}
		return "false"
	}
	return fmt.Sprintf("%v", v)
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
