// Package runserver is the self-contained single-run API server
// (local-first plan, Phase 2): `attractor run --ui` mounts this over
// the run's own logs dir. It serves the spec §9.5 surface for exactly
// one run, reading state from disk on every request — the engine
// writing events.jsonl is the single writer, this server only reads.
// Paths keep the daemon's /pipelines/{id}/... shape so hub and run
// speak one schema (D4).
package runserver

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/render"
	"github.com/allouis/attractor/internal/rundir"
	"github.com/allouis/attractor/internal/runview"
	"github.com/allouis/attractor/internal/webui"
)

// Server serves one run from its logs directory.
type Server struct {
	logsRoot string
	// Answer accepts an answer for a pending human-gate question; nil
	// when the process hosting this server has no live interviewer
	// (e.g. serving a finished run dir).
	Answer func(questionID, value, note string) error
	// Meta enriches spans with graph-derived node metadata (D5:
	// self-describing spans). Optional.
	Meta map[string]runview.NodeMeta
	// Token, when set, requires `Authorization: Bearer <Token>` on every
	// endpoint (local-first plan Phase 4: auth on the run server unlocks
	// remote runners). Empty means open — the loopback default.
	Token string
}

// New returns a Server rooted at the run's logs directory.
func New(logsRoot string) *Server {
	return &Server{logsRoot: logsRoot}
}

// Handler returns the full route surface. When requireToken is set and a
// Token is configured, every request must carry `Authorization: Bearer
// <Token>`. When it is not — a bind private by construction (loopback,
// tailnet) where a browser cannot send a bearer header — the surface is
// served without token auth. Passing the requirement in (rather than
// inferring it from Token) keeps the caller the single owner of which
// binds are authenticated.
func (s *Server) Handler(requireToken bool) http.Handler {
	mux := s.routes()
	if !requireToken || s.Token == "" {
		// Tokenless surface: a bearer cannot authenticate a browser, so
		// guard the mutable/read endpoints against the browser-borne
		// attacks Tailscale/loopback packet auth cannot stop.
		return guardBrowser(mux)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// guardBrowser protects a tokenless surface from browser-borne attacks
// that packet-level authentication (Tailscale, loopback) does not stop:
//   - DNS rebinding: it rejects a Host header that is not an IP literal or
//     localhost, since rebinding relies on an attacker hostname that
//     resolves to our address.
//   - CSRF: it rejects any request carrying a cross-origin Origin,
//     including the "simple" text/plain POST a page can make to the mutable
//     /answer endpoint without a preflight.
//
// Same-origin requests from the run's own UI (Origin == Host) and
// non-browser clients (no Origin) pass. The UI is advertised by IP, so a
// legitimate browser always sends an IP Host.
func guardBrowser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !originMatchesHost(o, r.Host) {
			http.Error(w, "cross-origin request forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed accepts only a Host that is an IP literal or localhost — a
// hostname that could be attacker-controlled (DNS rebinding) is rejected.
func hostAllowed(host string) bool {
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	return hostname == "localhost" || net.ParseIP(hostname) != nil
}

// originMatchesHost reports whether an Origin header is same-origin with
// the request Host (both are host[:port]).
func originMatchesHost(origin, host string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return u.Host == host
}

// routes builds the full route mux with no auth wrapping.
func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pipelines", s.list)
	mux.HandleFunc("GET /pipelines/{id}", s.doc)
	mux.HandleFunc("GET /pipelines/{id}/events", s.events)
	mux.HandleFunc("GET /pipelines/{id}/artifacts", s.listArtifacts)
	mux.HandleFunc("GET /pipelines/{id}/artifacts/{path...}", s.artifact)
	mux.HandleFunc("GET /pipelines/{id}/graph", s.graph)
	mux.HandleFunc("GET /pipelines/{id}/questions", s.questions)
	mux.HandleFunc("POST /pipelines/{id}/questions/{qid}/answer", s.answer)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	serveUI := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(webui.Waterfall)
	}
	mux.HandleFunc("GET /ui", serveUI)
	mux.HandleFunc("GET /ui/{id}", serveUI)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui", http.StatusFound)
	})
	return mux
}

// manifest reads run.json from the server's run dir. The server is
// tolerant per request: a missing or malformed file yields the zero
// Manifest (empty RunID = not readable yet), self-healing on the next
// poll, so the read error is deliberately discarded here.
func (s *Server) manifest() engine.Manifest {
	m, _ := rundir.ReadManifest(s.logsRoot)
	return m
}

// readEvents streams the server's run dir events.jsonl (Seq > since). A
// torn or unreadable log yields the events read so far; the poll model
// self-heals on the next request, so the read error is discarded.
func (s *Server) readEvents(since int64) []engine.Event {
	ev, _ := rundir.ReadEvents(s.logsRoot, since)
	return ev
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
	doc := runview.Document(m, s.readEvents(0))
	runview.AttachMeta(doc.Spans, s.Meta)
	writeJSON(w, doc)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkID(w, r); !ok {
		return
	}
	since := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		var err error
		if since, err = strconv.ParseInt(raw, 10, 64); err != nil {
			http.Error(w, "since: not an integer", http.StatusBadRequest)
			return
		}
	}
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

// graph renders the run's persisted topology (graph.dot) as SVG.
func (s *Server) graph(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.checkID(w, r); !ok {
		return
	}
	src, err := os.ReadFile(filepath.Join(s.logsRoot, "graph.dot"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	svg, err := render.SVG(src, "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(svg)
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
	// path.Clean the URL segment and reject any ".." segment (segment
	// compare, not substring — "notes..md" is a legal name).
	rel := path.Clean("/" + r.PathValue("path"))
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			http.NotFound(w, r)
			return
		}
	}
	// Serve artifacts inert: an agent- or project-controlled artifact
	// (e.g. a .html response) must not execute as active content under the
	// UI origin, or it could POST /answer with a same-origin request the
	// browser guard would permit. nosniff stops content-type sniffing;
	// CSP sandbox strips scripts and same-origin, so any embedded request
	// carries a null Origin the guard rejects.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	// Span-dir layout (A4): paths are derived forward from span
	// identity — no fallbacks needed.
	http.ServeFile(w, r, filepath.Join(s.logsRoot, filepath.FromSlash(rel)))
}
