package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_HeaderResponsive guards the responsive shell (ui-tailwind-spec
// T1): the header/nav migrate to token-mapped Tailwind utilities, the header
// becomes an opaque, stacked sticky bar (so nav stays reachable while scrolling
// and taps land on the nav rather than bleeding through to content beneath —
// the sticky-header tap-intercept fix), the nav wraps instead of overflowing at
// ≤640px, and the theme toggle keeps a tappable target. The 390px no-overflow
// and tap behaviour are verified by hand (agent-browser); here we guard the
// static markup + that the injected stylesheet actually defines the utilities.
func TestServer_UI_HeaderResponsive(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	header := sliceTag(t, page, "header")

	// Sticky, opaque, stacked bar: the tap-intercept fix. An opaque background
	// over a positive z-index keeps the header above scrolled content so nav
	// taps land on the nav, not on whatever scrolled underneath.
	for _, cls := range []string{"sticky", "top-0", "bg-surface-0"} {
		if !strings.Contains(header, cls) {
			t.Errorf("header missing Tailwind class %q (sticky tap-intercept fix):\n%s", cls, header)
		}
	}
	if !strings.Contains(header, "z-") {
		t.Errorf("header missing a z-index class (stacking):\n%s", header)
	}

	// Nav wraps at narrow widths instead of overflowing.
	if !strings.Contains(header, "flex-wrap") {
		t.Errorf("header/nav missing flex-wrap (collapse at <=640px):\n%s", header)
	}

	// All four tabs and the theme toggle survive the migration, toggle inside
	// the header and reachable.
	for _, href := range []string{`href="#items"`, `href="#runs"`, `href="#workflows"`, `href="#config"`} {
		if !strings.Contains(header, href) {
			t.Errorf("header missing nav link %q after migration:\n%s", href, header)
		}
	}
	if !strings.Contains(header, `id="theme-toggle"`) {
		t.Errorf("theme toggle not inside header (must stay reachable):\n%s", header)
	}

	// The injected stylesheet must define the utilities the markup relies on:
	// proves the committed tailwind.css was regenerated from the new classes,
	// so serveUI ships working CSS (dev/test parity).
	if !strings.Contains(page, "position:sticky") && !strings.Contains(page, "position: sticky") {
		t.Errorf("injected stylesheet defines no .sticky utility (stale tailwind.css?)")
	}
	if !strings.Contains(page, "@media (min-width:640px)") {
		t.Errorf("injected stylesheet has no 640px breakpoint (sm: utilities missing?)")
	}
}

// sliceTag returns the outer markup of the first <tag>…</tag> in src, so an
// assertion can scope to one element instead of matching a token page-wide.
func sliceTag(t *testing.T, src, tag string) string {
	t.Helper()
	start := strings.Index(src, "<"+tag)
	if start < 0 {
		t.Fatalf("<%s> not found in page", tag)
	}
	end := strings.Index(src[start:], "</"+tag+">")
	if end < 0 {
		t.Fatalf("no </%s> found in page", tag)
	}
	return src[start : start+end+len("</"+tag+">")]
}
