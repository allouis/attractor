// Package hub is the optional coordination point of the local-first
// architecture (plan Phase 4): a directory + scraper + archive for
// self-contained runs. Runs announce themselves once at start (a
// registration, not telemetry); the hub PULLS state from each live
// run's own API (/pipelines/{id}, /events?since=) — it never ingests a
// pushed event stream, so a hub outage cannot lose run data and a
// scrape failure doubles as the liveness signal. On completion a run
// ships a tar.gz of its run dir; the archive is the permanent record,
// served through the same API schema the live run used.
package hub

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/runview"
	"github.com/allouis/attractor/internal/webui"
)

// Hub tracks announced runs and archives. Dir is its state root:
// Dir/runs/{id}/ holds unpacked archives.
type Hub struct {
	dir    string
	client *http.Client
	// Launcher, when non-nil, enables POST /pipelines to spawn runs.
	Launcher *Launcher

	mu   sync.Mutex
	live map[string]*liveRun
}

// liveRun is one announced, not-yet-archived run.
type liveRun struct {
	RunID string `json:"run_id"`
	URL   string `json:"url"`
	// token authenticates scrapes/proxies against the run's server
	// (Bearer). Empty for open loopback runs.
	token string
	// lastDoc is the most recent successful scrape; kept so an
	// unreachable run still shows its last known state.
	lastDoc   *runview.RunDoc
	reachable bool
	lastSeen  time.Time
}

// hubRunSummary is the /runs listing row: scrape state alongside the
// run summary.
type hubRunSummary struct {
	RunID     string    `json:"run_id"`
	URL       string    `json:"url,omitempty"`
	Status    string    `json:"status"`
	Reachable bool      `json:"reachable"`
	Archived  bool      `json:"archived"`
	LastSeen  time.Time `json:"last_seen,omitzero"`
}

// New returns a Hub rooted at dir.
func New(dir string) *Hub {
	return &Hub{
		dir:    dir,
		client: &http.Client{Timeout: 10 * time.Second},
		live:   map[string]*liveRun{},
	}
}

// Handler returns the hub's HTTP surface.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /announce", h.announce)
	mux.HandleFunc("GET /runs", h.listRuns)
	mux.HandleFunc("GET /pipelines", h.listPipelines)
	mux.HandleFunc("GET /pipelines/{id}", h.getPipeline)
	mux.HandleFunc("GET /pipelines/{id}/events", h.proxyOrArchive("/events"))
	mux.HandleFunc("GET /pipelines/{id}/questions", h.proxyOrArchive("/questions"))
	mux.HandleFunc("GET /pipelines/{id}/artifacts", h.proxyOrArchive("/artifacts"))
	mux.HandleFunc("GET /pipelines/{id}/artifacts/{path...}", h.artifact)
	mux.HandleFunc("POST /pipelines/{id}/questions/{qid}/answer", h.answer)
	mux.HandleFunc("POST /pipelines/{id}/archive", h.archive)
	if h.Launcher != nil {
		mux.HandleFunc("POST /pipelines", h.Launcher.handleLaunch)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /ui", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(webui.HubIndex)
	})
	mux.HandleFunc("GET /ui/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(webui.Waterfall)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui", http.StatusFound)
	})
	return mux
}

// artifact serves one run-dir file: proxied from the live run, or read
// from the unpacked archive for finished runs (same traversal guard as
// the run server: '..' path segments are rejected).
func (h *Hub) artifact(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rel := path.Clean("/" + r.PathValue("path"))
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			http.NotFound(w, r)
			return
		}
	}
	if lr, ok := h.liveRun(id); ok && lr.URL != "" {
		resp, err := h.runGet(lr, lr.URL+"/pipelines/"+id+"/artifacts"+rel)
		if err == nil {
			defer resp.Body.Close()
			w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}
	}
	http.ServeFile(w, r, filepath.Join(h.runDir(id), filepath.FromSlash(rel)))
}

// Run starts the background scrape loop until stop is closed.
func (h *Hub) Run(stop <-chan struct{}, interval time.Duration) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			h.ScrapeAll()
		}
	}
}

// announceBody is the one-shot registration a run posts at start. The
// optional token is what the hub presents back to the run's server
// (Bearer) — auth on the run server is what unlocks remote runners.
type announceBody struct {
	RunID string `json:"run_id"`
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

func (h *Hub) announce(w http.ResponseWriter, r *http.Request) {
	var body announceBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RunID == "" || body.URL == "" {
		http.Error(w, "want {run_id, url}", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	prev := h.live[body.RunID]
	lr := &liveRun{RunID: body.RunID, URL: strings.TrimRight(body.URL, "/"), token: body.Token}
	if prev != nil {
		lr.lastDoc = prev.lastDoc
	}
	h.live[body.RunID] = lr
	h.mu.Unlock()
	// Scrape synchronously so the run is listed (with state) the moment
	// the announce returns; one bounded GET.
	h.scrape(lr)
	w.WriteHeader(http.StatusNoContent)
}

// ScrapeAll pulls every live run's document once. Exposed for the
// scrape loop and tests.
func (h *Hub) ScrapeAll() {
	h.mu.Lock()
	runs := make([]*liveRun, 0, len(h.live))
	for _, lr := range h.live {
		runs = append(runs, lr)
	}
	h.mu.Unlock()
	for _, lr := range runs {
		h.scrape(lr)
	}
}

// runGet issues an authenticated GET against a live run's server.
func (h *Hub) runGet(lr *liveRun, url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	if lr.token != "" {
		req.Header.Set("Authorization", "Bearer "+lr.token)
	}
	return h.client.Do(req)
}

func (h *Hub) scrape(lr *liveRun) {
	resp, err := h.runGet(lr, lr.URL+"/pipelines/"+lr.RunID)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		lr.reachable = false
		return
	}
	defer resp.Body.Close()
	var doc runview.RunDoc
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		lr.reachable = false
		return
	}
	lr.lastDoc = &doc
	lr.reachable = true
	lr.lastSeen = time.Now()
}

func (h *Hub) listRuns(w http.ResponseWriter, r *http.Request) {
	out := []hubRunSummary{}
	seen := map[string]bool{}
	h.mu.Lock()
	for _, lr := range h.live {
		s := hubRunSummary{RunID: lr.RunID, URL: lr.URL, Reachable: lr.reachable, LastSeen: lr.lastSeen}
		if lr.lastDoc != nil {
			s.Status = lr.lastDoc.Status
		}
		out = append(out, s)
		seen[lr.RunID] = true
	}
	h.mu.Unlock()
	for _, id := range h.archivedIDs() {
		if seen[id] {
			continue
		}
		s := hubRunSummary{RunID: id, Archived: true}
		if doc, err := h.archivedDoc(id); err == nil {
			s.Status = doc.Status
		}
		out = append(out, s)
	}
	writeJSON(w, out)
}

func (h *Hub) listPipelines(w http.ResponseWriter, r *http.Request) {
	out := []runview.RunDoc{}
	seen := map[string]bool{}
	h.mu.Lock()
	for _, lr := range h.live {
		if lr.lastDoc != nil {
			doc := *lr.lastDoc
			doc.Spans = nil
			out = append(out, doc)
			seen[lr.RunID] = true
		}
	}
	h.mu.Unlock()
	for _, id := range h.archivedIDs() {
		if seen[id] {
			continue
		}
		if doc, err := h.archivedDoc(id); err == nil {
			doc.Spans = nil
			out = append(out, doc)
		}
	}
	writeJSON(w, out)
}

func (h *Hub) getPipeline(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if lr, ok := h.liveRun(id); ok {
		// Prefer a fresh scrape; fall back to the last known doc.
		h.scrape(lr)
		h.mu.Lock()
		doc := lr.lastDoc
		h.mu.Unlock()
		if doc != nil {
			writeJSON(w, doc)
			return
		}
	}
	if doc, err := h.archivedDoc(id); err == nil {
		writeJSON(w, doc)
		return
	}
	http.NotFound(w, r)
}

// proxyOrArchive forwards a GET to the live run's same-schema endpoint,
// or serves the archived copy for finished runs.
func (h *Hub) proxyOrArchive(suffix string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if lr, ok := h.liveRun(id); ok && lr.URL != "" {
			target := lr.URL + "/pipelines/" + id + suffix
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			resp, err := h.runGet(lr, target)
			if err == nil {
				defer resp.Body.Close()
				w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
				w.WriteHeader(resp.StatusCode)
				_, _ = io.Copy(w, resp.Body)
				return
			}
			// Fall through to the archive on scrape failure.
		}
		if suffix == "/events" {
			path := filepath.Join(h.runDir(id), "events.jsonl")
			if _, err := os.Stat(path); err == nil {
				w.Header().Set("Content-Type", "application/x-ndjson")
				serveEventsSince(w, path, r.URL.Query().Get("since"))
				return
			}
		}
		http.NotFound(w, r)
	}
}

// answer forwards a gate answer to the live run — a central UI proxies
// to the run's own /answer surface.
func (h *Hub) answer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	lr, ok := h.liveRun(id)
	if !ok || lr.URL == "" {
		http.NotFound(w, r)
		return
	}
	target := lr.URL + "/pipelines/" + id + "/questions/" + r.PathValue("qid") + "/answer"
	req, err := http.NewRequest("POST", target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	if lr.token != "" {
		req.Header.Set("Authorization", "Bearer "+lr.token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// archive accepts the run's tar.gz, unpacks it under Dir/runs/{id}, and
// drops the run from the live set. Idempotent: re-shipping overwrites.
func (h *Hub) archive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dst := h.runDir(id)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := untarInto(dst, r.Body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	delete(h.live, id)
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) liveRun(id string) (*liveRun, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lr, ok := h.live[id]
	return lr, ok
}

func (h *Hub) runDir(id string) string {
	return filepath.Join(h.dir, "runs", filepath.Base(id))
}

func (h *Hub) archivedIDs() []string {
	entries, err := os.ReadDir(filepath.Join(h.dir, "runs"))
	if err != nil {
		return nil
	}
	var out []string
	for _, ent := range entries {
		if ent.IsDir() {
			out = append(out, ent.Name())
		}
	}
	return out
}

// archivedDoc folds an archived run dir into the same enriched document
// a live run serves (D4: one schema either way).
func (h *Hub) archivedDoc(id string) (runview.RunDoc, error) {
	dir := h.runDir(id)
	data, err := os.ReadFile(filepath.Join(dir, "run.json"))
	if err != nil {
		return runview.RunDoc{}, err
	}
	var m engine.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return runview.RunDoc{}, err
	}
	events, err := readEventsFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return runview.RunDoc{}, err
	}
	return runview.Document(m, events), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// untarInto unpacks a tar.gz stream into dst, rejecting entries that
// would escape it.
func untarInto(dst string, r io.Reader) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	cleanDst := filepath.Clean(dst)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target := filepath.Join(cleanDst, filepath.FromSlash(hdr.Name))
		if target != cleanDst && !strings.HasPrefix(target, cleanDst+string(filepath.Separator)) {
			return fmt.Errorf("tar entry escapes archive root: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // archives are size-bounded run dirs from trusted runs
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// Symlinks and specials are dropped: a run dir archive has no
			// legitimate use for them and they are the escape vector.
		}
	}
}
