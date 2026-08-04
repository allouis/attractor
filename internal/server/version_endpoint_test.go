package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/version"
)

// TestVersionEndpoint_JSONShape: GET /version returns the build's version
// and revision as a stable JSON object, sourced from internal/version.
func TestVersionEndpoint_JSONShape(t *testing.T) {
	rec := serveConfig(t, http.MethodGet, "/version", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got struct {
		Version  string `json:"version"`
		Revision string `json:"revision"`
	}
	mustNil(t, json.Unmarshal(rec.Body.Bytes(), &got))
	if got.Version != version.Number {
		t.Errorf("version = %q, want %q", got.Version, version.Number)
	}
	if got.Revision == "" {
		t.Error("revision field not populated")
	}
}

// TestVersionEndpoint_BypassesAuth: /version is reachable without a bearer
// token even when one is configured — same auth bypass as /healthz. The
// httptest request carries the default non-loopback RemoteAddr, which would
// otherwise be gated.
func TestVersionEndpoint_BypassesAuth(t *testing.T) {
	srv := New(Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     t.TempDir(),
		MakeHandlers: DefaultHandlers(handler.Codergen{}),
		AuthToken:    "secrettoken",
	})
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	srv.httpsrv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (auth should be bypassed)", rec.Code)
	}
}
