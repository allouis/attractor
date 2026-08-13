package hub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/runserver"
	"github.com/allouis/attractor/internal/runview"
)

// startRun materializes a run dir (reusing the runserver test fixture
// shape) and serves it, returning the run's base URL and id.
func startRun(t *testing.T, status string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	writeRunDir(t, dir, "r1", status)
	rs := runserver.New(dir)
	ts := httptest.NewServer(rs.Handler())
	t.Cleanup(ts.Close)
	return ts.URL, "r1"
}

func newHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	h := New(t.TempDir())
	ts := httptest.NewServer(h.Handler())
	t.Cleanup(ts.Close)
	return h, ts
}

func postJSON(t *testing.T, url string, v any) *http.Response {
	t.Helper()
	data, _ := json.Marshal(v)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A run announces once at start (a registration, not telemetry); the
// hub then lists it by scraping the run's own API.
func TestHub_AnnounceThenList(t *testing.T) {
	runURL, runID := startRun(t, "running")
	_, ts := newHub(t)

	resp := postJSON(t, ts.URL+"/announce", map[string]string{"run_id": runID, "url": runURL})
	if resp.StatusCode != 204 {
		t.Fatalf("announce status %d", resp.StatusCode)
	}

	list := getJSON[[]runview.RunDoc](t, ts.URL+"/pipelines")
	if len(list) != 1 || list[0].RunID != runID {
		t.Fatalf("list = %+v, want the announced run", list)
	}

	doc := getJSON[runview.RunDoc](t, ts.URL+"/pipelines/"+runID)
	if doc.RunID != runID || len(doc.Spans) == 0 {
		t.Fatalf("doc not scraped through: %+v", doc)
	}
}

// The hub proxies the events cursor API to the live run — one schema
// end to end.
func TestHub_ProxiesEvents(t *testing.T) {
	runURL, runID := startRun(t, "running")
	_, ts := newHub(t)
	postJSON(t, ts.URL+"/announce", map[string]string{"run_id": runID, "url": runURL})

	resp, err := http.Get(ts.URL + "/pipelines/" + runID + "/events?since=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("events not proxied: %q", buf.String())
	}
	var ev engine.Event
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil || ev.Seq <= 1 {
		t.Fatalf("since cursor not honored: %q", lines[0])
	}
}

// An unreachable run is not an error: it stays listed with its last
// scraped state, flagged unreachable (scrape failure IS the liveness
// signal — a hub outage or network cut loses nothing).
func TestHub_UnreachableRunStaysListed(t *testing.T) {
	runURL, runID := startRun(t, "running")
	h, ts := newHub(t)
	postJSON(t, ts.URL+"/announce", map[string]string{"run_id": runID, "url": runURL})
	h.ScrapeAll() // capture state while live

	// Kill the run server: scrape now fails.
	// (httptest cleanup closes at test end; simulate by re-announcing a
	// dead URL.)
	postJSON(t, ts.URL+"/announce", map[string]string{"run_id": runID, "url": "http://127.0.0.1:1"})
	h.ScrapeAll()

	list := getJSON[[]hubRunSummary](t, ts.URL+"/runs")
	if len(list) != 1 {
		t.Fatalf("run vanished on scrape failure: %+v", list)
	}
	if list[0].Reachable {
		t.Fatalf("dead run still marked reachable: %+v", list[0])
	}
}

// Archive-on-complete: the run ships a tar.gz of its run dir; the hub
// stores it as the permanent record and serves the archived doc even
// after the run server is gone.
func TestHub_ArchiveBecomesPermanentRecord(t *testing.T) {
	dir := t.TempDir()
	writeRunDir(t, dir, "r9", "completed")
	_, ts := newHub(t)

	var buf bytes.Buffer
	if err := TarRunDir(dir, &buf); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/pipelines/r9/archive", &buf)
	req.Header.Set("Content-Type", "application/gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 204 {
		t.Fatalf("archive status %d", resp.StatusCode)
	}

	doc := getJSON[runview.RunDoc](t, ts.URL+"/pipelines/r9")
	if doc.RunID != "r9" || doc.Status != "completed" || len(doc.Spans) == 0 {
		t.Fatalf("archived doc wrong: %+v", doc)
	}
	// Events replay from the archive too.
	resp2, err := http.Get(ts.URL + "/pipelines/r9/events?since=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	b := new(bytes.Buffer)
	b.ReadFrom(resp2.Body)
	if !strings.Contains(b.String(), "pipeline_completed") {
		t.Fatalf("archived events not served: %q", b.String())
	}
}

// The archive tar must not escape the hub's run dir (hostile paths).
func TestHub_ArchiveRejectsPathEscape(t *testing.T) {
	_, ts := newHub(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	data := []byte("evil")
	tw.WriteHeader(&tar.Header{Name: "../../evil.txt", Mode: 0o644, Size: int64(len(data))})
	tw.Write(data)
	tw.Close()
	gz.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/pipelines/rX/archive", &buf)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode == 204 {
		t.Fatal("hostile tar accepted")
	}
}

func getJSON[T any](t *testing.T, url string) T {
	t.Helper()
	var out T
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The hub serves a browser UI: an index listing runs, and the shared
// waterfall page per run — the page drives off the same /pipelines API
// the hub already exposes for live and archived runs alike.
func TestHub_ServesUI(t *testing.T) {
	_, ts := newHub(t)
	for _, path := range []string{"/ui", "/ui/some-run"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body := new(bytes.Buffer)
		body.ReadFrom(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: status %d", path, resp.StatusCode)
		}
		if !strings.Contains(body.String(), "<!doctype html>") {
			t.Fatalf("GET %s: not a page: %.80s", path, body.String())
		}
	}
	// / redirects to the index.
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/ui" {
		t.Fatalf("/ should land on /ui, landed on %s", resp.Request.URL.Path)
	}
}

// The waterfall's detail panel fetches /pipelines/{id}/artifacts/…;
// the hub proxies those to live runs and serves them from the archive
// for finished ones.
func TestHub_ArtifactsProxyAndArchive(t *testing.T) {
	runURL, runID := startRun(t, "running")
	_, ts := newHub(t)
	postJSON(t, ts.URL+"/announce", map[string]string{"run_id": runID, "url": runURL})

	// Live: proxied through to the run's own server.
	resp, err := http.Get(ts.URL + "/pipelines/" + runID + "/artifacts/run.json")
	if err != nil {
		t.Fatal(err)
	}
	body := new(bytes.Buffer)
	body.ReadFrom(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(body.String(), runID) {
		t.Fatalf("live artifact proxy failed: %d %q", resp.StatusCode, body.String())
	}

	// Archived: served from the unpacked archive.
	dir := t.TempDir()
	writeRunDir(t, dir, "r7", "completed")
	var buf bytes.Buffer
	if err := TarRunDir(dir, &buf); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/pipelines/r7/archive", &buf)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 204 {
		t.Fatalf("archive upload: %v %v", err, resp)
	}
	resp2, err := http.Get(ts.URL + "/pipelines/r7/artifacts/run.json")
	if err != nil {
		t.Fatal(err)
	}
	body2 := new(bytes.Buffer)
	body2.ReadFrom(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 200 || !strings.Contains(body2.String(), "r7") {
		t.Fatalf("archived artifact serve failed: %d %q", resp2.StatusCode, body2.String())
	}
	// Escapes rejected.
	resp3, _ := http.Get(ts.URL + "/pipelines/r7/artifacts/../../etc/passwd")
	if resp3 != nil {
		resp3.Body.Close()
		if resp3.StatusCode == 200 {
			t.Fatal("artifact path escape served")
		}
	}
}

// The artifacts LISTING must also survive archival — the waterfall's
// detail panel drives its visit tabs and tool-call list off it.
func TestHub_ArchivedArtifactsListing(t *testing.T) {
	dir := t.TempDir()
	writeRunDir(t, dir, "r8", "completed")
	if err := os.MkdirAll(filepath.Join(dir, "work", "v1", "tool_calls"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "work", "v1", "tool_calls", "001-tool_call.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ts := newHub(t)
	var buf bytes.Buffer
	if err := TarRunDir(dir, &buf); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("POST", ts.URL+"/pipelines/r8/archive", &buf)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 204 {
		t.Fatalf("archive: %v %v", err, resp)
	}
	files := getJSON[[]string](t, ts.URL+"/pipelines/r8/artifacts")
	found := false
	for _, f := range files {
		if f == "work/v1/tool_calls/001-tool_call.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("archived listing missing tool call file: %v", files)
	}
}
