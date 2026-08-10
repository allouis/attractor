package server

import (
	"net/http"
	"testing"
)

// TestRootRedirectsToUI: the app lives under /ui, so the bare root redirects
// there (a Tailscale-served hostname proxies "/", which otherwise 404s). The
// {$} pattern must match ONLY exactly "/", so an unknown path still 404s rather
// than redirecting.
func TestRootRedirectsToUI(t *testing.T) {
	rec := serveConfig(t, http.MethodGet, "/", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET / = %d, want 302 to the UI; body=%s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/ui" {
		t.Errorf("Location = %q, want /ui", loc)
	}
	if r := serveConfig(t, http.MethodGet, "/nope", nil); r.Code != http.StatusNotFound {
		t.Errorf("GET /nope = %d, want 404 (only exact / redirects)", r.Code)
	}
}
