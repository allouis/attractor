package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// stageDetail is the inline output of a single stage (tui-spec T2), read
// from the stage's log dir: prompt.md, response.md, status.json, and each
// file under tool_calls/. It gives both UIs inline text instead of raw
// file links.
type stageDetail struct {
	Status    string            `json:"status"`
	Prompt    string            `json:"prompt"`
	Response  string            `json:"response"`
	ExitCode  string            `json:"exit_code,omitempty"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
}

// getStage serves GET /pipelines/{id}/stages/{node}. It 404s for an unknown
// run or a stage that has no log dir yet.
func (s *Server) getStage(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	stageDir := filepath.Join(run.logsRoot, r.PathValue("node"))
	if info, err := os.Stat(stageDir); err != nil || !info.IsDir() {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, readStageDetail(stageDir))
}

// readStageDetail collects a stage's inline artifacts from its log dir.
// Missing files yield empty fields; the caller has already confirmed the
// dir exists.
func readStageDetail(stageDir string) stageDetail {
	readFile := func(name string) string {
		data, _ := os.ReadFile(filepath.Join(stageDir, name))
		return string(data)
	}
	d := stageDetail{
		Prompt:    readFile("prompt.md"),
		Response:  readFile("response.md"),
		ToolCalls: readToolCalls(filepath.Join(stageDir, "tool_calls")),
	}
	// status.json is the engine.Outcome; its status string lives under the
	// "outcome" key, and a tool node records its process exit code in the
	// outcome context (tool.exit_code) — surfaced so the inspector can show a
	// tool's exit code. Absent (stage still running) leaves both empty.
	if data, err := os.ReadFile(filepath.Join(stageDir, "status.json")); err == nil {
		var st struct {
			Outcome        string            `json:"outcome"`
			ContextUpdates map[string]string `json:"context_updates"`
		}
		if json.Unmarshal(data, &st) == nil {
			d.Status = st.Outcome
			d.ExitCode = st.ContextUpdates["tool.exit_code"]
		}
	}
	return d
}

// readToolCalls returns each tool_calls/<n>-<hook>.json file as raw JSON,
// ordered by the numeric prefix the ingest server stamps (so 10 sorts after
// 2, unlike lexical order).
func readToolCalls(dir string) []json.RawMessage {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []json.RawMessage{}
	}
	names := make([]string, 0, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasSuffix(ent.Name(), ".json") {
			names = append(names, ent.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool {
		ni, oki := toolCallSeq(names[i])
		nj, okj := toolCallSeq(names[j])
		if oki && okj && ni != nj {
			return ni < nj
		}
		return names[i] < names[j]
	})
	out := make([]json.RawMessage, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		out = append(out, json.RawMessage(data))
	}
	return out
}

// toolCallSeq parses the leading integer of a "<n>-<hook>.json" filename.
func toolCallSeq(name string) (int, bool) {
	i := strings.IndexByte(name, '-')
	if i <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(name[:i])
	if err != nil {
		return 0, false
	}
	return n, true
}
