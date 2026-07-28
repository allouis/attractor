package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_Theme guards the design foundation (web-ui-spec W1): the
// page ships a CSS token system (surface/text ramp, spacing scale, the
// semantic state palette) and dark/light support via prefers-color-scheme
// plus a persisted manual toggle. These are static markers in the served
// HTML; the runtime persistence behaviour is verified separately.
func TestServer_UI_Theme(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	// Token ramp + scale: every view must consume tokens, not ad-hoc values.
	for _, tok := range []string{"--surface-0", "--text", "--text-muted", "--border", "--space-", "--font-"} {
		if !strings.Contains(page, tok) {
			t.Errorf("page missing design token %q", tok)
		}
	}
	// Full semantic state palette (the load-bearing colour decision).
	for _, state := range []string{"queued", "running", "success", "failed", "needs-human", "retrying", "cancelled"} {
		if !strings.Contains(page, "--state-"+state) {
			t.Errorf("page missing state token --state-%s", state)
		}
	}
	// Dark/light: prefers-color-scheme auto + an explicit data-theme hook
	// for the manual toggle control.
	for _, marker := range []string{"prefers-color-scheme", "data-theme", `id="theme-toggle"`} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing theme marker %q", marker)
		}
	}
}
