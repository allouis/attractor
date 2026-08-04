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
	// The sm: breakpoint. Tailwind v3 emitted 640px, v4 emits the rem
	// equivalent (40rem); accept either so the guard tracks the utility.
	if !strings.Contains(page, "@media (min-width:640px)") && !strings.Contains(page, "@media (min-width:40rem)") {
		t.Errorf("injected stylesheet has no 640px breakpoint (sm: utilities missing?)")
	}
}

// TestServer_UI_AppShellMobileMenu guards the app-shell/nav swap (ui-tailwind-spec
// T6d): the header adopts the Tailwind Plus stacked app-shell — a desktop inline
// nav that collapses at <640px behind a real mobile menu. The menu is a
// hamburger button toggling a stacked disclosure panel; hash routing + the theme
// toggle stay wired. Behaviour (tap-to-open, 390px no-overflow, routing dismiss)
// is verified by hand (agent-browser); here we guard the static markup + that the
// injected stylesheet defines the responsive utilities the swap relies on, with
// no stock gray/indigo palette leaking through.
func TestServer_UI_AppShellMobileMenu(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	header := sliceTag(t, page, "header")

	// Desktop nav is inline at >=640px, collapsed below it: hidden by default,
	// sm:flex on the wide viewport.
	nav := sliceTag(t, header, "nav")
	for _, cls := range []string{"hidden", "sm:flex"} {
		if !strings.Contains(nav, cls) {
			t.Errorf("desktop nav missing %q (collapse below sm:):\n%s", cls, nav)
		}
	}

	// The hamburger: shown only <640px (sm:hidden), wired to the menu it
	// controls with an aria-expanded state the toggle flips.
	if !strings.Contains(header, `id="mobile-menu-button"`) {
		t.Errorf("header missing the mobile-menu button:\n%s", header)
	}
	for _, want := range []string{"sm:hidden", `aria-controls="mobile-menu"`, "aria-expanded"} {
		if !strings.Contains(header, want) {
			t.Errorf("mobile-menu button missing %q:\n%s", want, header)
		}
	}

	// The disclosure panel: a stacked nav, hidden until toggled and never shown
	// at >=640px (the inline nav owns the desktop), carrying all four tabs.
	// Scope from its id to the next </nav> (it is the second, trailing nav).
	mi := strings.Index(header, `id="mobile-menu"`)
	if mi < 0 {
		t.Fatalf("no mobile-menu panel in header:\n%s", header)
	}
	menu := header[mi : mi+strings.Index(header[mi:], "</nav>")]
	if !strings.Contains(menu, "hidden") {
		t.Errorf("mobile menu not hidden by default:\n%s", menu)
	}
	if !strings.Contains(menu, "sm:hidden") {
		t.Errorf("mobile menu shows at desktop widths (missing sm:hidden):\n%s", menu)
	}
	for _, href := range []string{`href="#items"`, `href="#runs"`, `href="#workflows"`, `href="#config"`} {
		if !strings.Contains(menu, href) {
			t.Errorf("mobile menu missing nav link %q:\n%s", href, menu)
		}
	}

	// Theme toggle survives the swap and stays in the header (reachable on both
	// layouts).
	if !strings.Contains(header, `id="theme-toggle"`) {
		t.Errorf("theme toggle not inside header after app-shell swap:\n%s", header)
	}

	// Token-remapped: no stock Tailwind Plus gray/indigo palette in the shell.
	for _, bad := range []string{"gray-", "indigo-"} {
		if strings.Contains(header, bad) {
			t.Errorf("header leaked a hardcoded palette %q:\n%s", bad, header)
		}
	}

	// The injected stylesheet must define sm:flex (new with the desktop inline
	// nav) — proves the committed tailwind.css was regenerated from the swap.
	if !strings.Contains(page, `.sm\:flex`) {
		t.Errorf("injected stylesheet has no .sm\\:flex utility (stale tailwind.css?)")
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
