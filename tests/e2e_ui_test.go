package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI confirms the embedded single-page UI is served at /ui
// (service-spec §4): one static HTML page, no build step.
func TestServer_UI(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /ui status=%d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET /ui content-type=%q, want text/html", ct)
	}
	// The page must carry the mount points the JS binds to.
	for _, marker := range []string{"id=\"run-list\"", "id=\"run-view\"", "attractor"} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("GET /ui body missing %q", marker)
		}
	}
}
