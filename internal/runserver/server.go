// Package runserver is the self-contained single-run API server
// (local-first plan, Phase 2): `attractor run --ui` mounts this over
// the run's own logs dir. It serves the spec §9.5 surface for exactly
// one run, reading state from disk on every request — the engine
// writing events.jsonl is the single writer, this server only reads.
// Paths keep the daemon's /pipelines/{id}/... shape so hub and run
// speak one schema (D4).
package runserver

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/runview"
)

// Server serves one run from its logs directory.
type Server struct {
	logsRoot string
	// Answer accepts an answer for a pending human-gate question; nil
	// when the process hosting this server has no live interviewer
	// (e.g. serving a finished run dir).
	Answer func(questionID, value, note string) error
}

// New returns a Server rooted at the run's logs directory.
func New(logsRoot string) *Server {
	return &Server{logsRoot: logsRoot}
}

// Handler returns the HTTP handler with the full route surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pipelines", s.list)
	mux.HandleFunc("GET /pipelines/{id}", s.doc)
	mux.HandleFunc("GET /pipelines/{id}/events", s.events)
	mux.HandleFunc("GET /pipelines/{id}/artifacts", s.listArtifacts)
	mux.HandleFunc("GET /pipelines/{id}/artifacts/{path...}", s.artifact)
	mux.HandleFunc("GET /pipelines/{id}/questions", s.questions)
	mux.HandleFunc("POST /pipelines/{id}/questions/{qid}/answer", s.answer)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	return mux
}

// manifest reads run.json. The zero Manifest (with empty RunID) means
// the run dir is not readable yet.
func (s *Server) manifest() engine.Manifest {
	var m engine.Manifest
	data, err := os.ReadFile(filepath.Join(s.logsRoot, "run.json"))
	if err != nil {
		return m
	}
	_ = json.Unmarshal(data, &m)
	return m
}

// readEvents streams events.jsonl, returning events with Seq > since.
func (s *Server) readEvents(since int64) []engine.Event {
	f, err := os.Open(filepath.Join(s.logsRoot, "events.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []engine.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ev engine.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Seq > since {
			out = append(out, ev)
		}
	}
	return out
}

// checkID verifies the {id} path segment names this run. Empty RunID in
// run.json (file not written yet) matches nothing.
func (s *Server) checkID(w http.ResponseWriter, r *http.Request) (engine.Manifest, bool) {
	m := s.manifest()
	if id := r.PathValue("id"); m.RunID == "" || id != m.RunID {
		http.NotFound(w, r)
		return m, false
	}
	return m, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	m := s.manifest()
	if m.RunID == "" {
		writeJSON(w, []runview.RunDoc{})
		return
	}
	doc := runview.Document(m, s.readEvents(0))
	doc.Spans = nil // list rows are summaries; the detail doc has spans
	writeJSON(w, []runview.RunDoc{doc})
}

func (s *Server) doc(w http.ResponseWriter, r *http.Request) {
	m, ok := s.checkID(w, r)
	if !ok {
		return
	}
	writeJSON(w, runview.Document(m, s.readEvents(0)))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkID(w, r); !ok {
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	for _, ev := range s.readEvents(since) {
		_ = enc.Encode(ev)
	}
}

func (s *Server) questions(w http.ResponseWriter, r *http.Request) {
	m, ok := s.checkID(w, r)
	if !ok {
		return
	}
	doc := runview.Document(m, s.readEvents(0))
	if doc.PendingQuestions == nil {
		writeJSON(w, []runview.PendingQuestion{})
		return
	}
	writeJSON(w, doc.PendingQuestions)
}

// answerBody is the POST /questions/{qid}/answer payload.
type answerBody struct {
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkID(w, r); !ok {
		return
	}
	if s.Answer == nil {
		http.Error(w, "no live interviewer on this server", http.StatusConflict)
		return
	}
	var body answerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Answer(r.PathValue("qid"), body.Value, body.Note); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listArtifacts returns the run dir's file tree (relative paths).
func (s *Server) listArtifacts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkID(w, r); !ok {
		return
	}
	var files []string
	_ = filepath.WalkDir(s.logsRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(s.logsRoot, p)
		if err == nil {
			files = append(files, rel)
		}
		return nil
	})
	writeJSON(w, files)
}

func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkID(w, r); !ok {
		return
	}
	// path.Clean the URL segment and reject anything escaping the root.
	rel := path.Clean("/" + r.PathValue("path"))
	if strings.Contains(rel, "..") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.logsRoot, filepath.FromSlash(rel)))
}
