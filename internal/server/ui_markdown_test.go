package server

import (
	"strings"
	"testing"
)

// TestRenderMarkdownSubset: the shared prose renderer (ui-run-view-v3 R2)
// covers the safe subset — headings, bold/italic, inline + fenced code, lists,
// links — and emits only the tags it produces.
func TestRenderMarkdownSubset(t *testing.T) {
	html := evalUI(t, "renderMarkdown('# Title\\n\\nsome **bold**, *em* and `code`\\n\\n- one\\n- two')")
	for _, want := range []string{
		"<h1 class=\"md-h\">Title</h1>",
		"<strong>bold</strong>",
		"<em>em</em>",
		"<code class=\"md-inline-code\">code</code>",
		"<ul class=\"md-list\"><li>one</li><li>two</li></ul>",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("markdown missing %q:\n%s", want, html)
		}
	}
}

// TestRenderMarkdownFencedCode: a fenced block renders verbatim in a <pre><code>
// and its contents are never inline-processed (markers stay literal).
func TestRenderMarkdownFencedCode(t *testing.T) {
	html := evalUI(t, "renderMarkdown('```\\nno **bold** here\\n```')")
	if !strings.Contains(html, "<pre class=\"md-code overflow-x-auto\"><code>") {
		t.Errorf("fenced block not rendered as code:\n%s", html)
	}
	if strings.Contains(html, "<strong>") {
		t.Errorf("fenced code was inline-processed:\n%s", html)
	}
}

// TestRenderMarkdownEscapesXSS: prose is attacker-controlled (agent output) and
// must be HTML-escaped first — no raw tag, no javascript: href survives.
func TestRenderMarkdownEscapesXSS(t *testing.T) {
	html := evalUI(t, "renderMarkdown('<img src=x onerror=alert(1)>\\n\\n[click](javascript:alert(2))')")
	for _, bad := range []string{"<img src=x", "javascript:alert"} {
		if strings.Contains(html, bad) {
			t.Errorf("markdown leaked attacker markup %q:\n%s", bad, html)
		}
	}
	if !strings.Contains(html, "&lt;img") {
		t.Errorf("html not escaped:\n%s", html)
	}
	// A javascript: link collapses to a safe href.
	if !strings.Contains(html, "href=\"#\"") {
		t.Errorf("unsafe link not neutralised:\n%s", html)
	}
}

// TestRenderMarkdownDigitsNotCode: the inline-code placeholder must not collide
// with bare digits in prose (a naive " N " placeholder would corrupt "step 5").
func TestRenderMarkdownDigitsNotCode(t *testing.T) {
	html := evalUI(t, "renderMarkdown('step 5 done')")
	if strings.Contains(html, "md-inline-code") {
		t.Errorf("bare digit misrendered as inline code:\n%s", html)
	}
	if !strings.Contains(html, "step 5 done") {
		t.Errorf("prose text lost:\n%s", html)
	}
}

// TestAnsiColorsAndBold: the SGR subset maps 16-colour fg + bold/dim to
// theme-aware classes.
func TestAnsiColorsAndBold(t *testing.T) {
	html := evalUI(t, "ansiToHtml('\\x1b[32mOK\\x1b[0m \\x1b[1;31mFAIL\\x1b[0m \\x1b[2mdim\\x1b[0m')")
	for _, want := range []string{
		`<span class="ansi-grn">OK</span>`,
		`<span class="ansi-bold ansi-red">FAIL</span>`,
		`<span class="ansi-dim">dim</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("ansi missing %q:\n%s", want, html)
		}
	}
}

// TestAnsi256: 256-colour and truecolour fg ride an inline rgb() (ANSI colours
// are absolute), not a token class.
func TestAnsi256(t *testing.T) {
	html := evalUI(t, "ansiToHtml('\\x1b[38;5;196mX\\x1b[0m')")
	if !strings.Contains(html, "style=\"color:rgb(") {
		t.Errorf("256-colour not rendered as rgb:\n%s", html)
	}
	tc := evalUI(t, "ansiToHtml('\\x1b[38;2;10;20;30mY\\x1b[0m')")
	if !strings.Contains(tc, "rgb(10,20,30)") {
		t.Errorf("truecolour not rendered:\n%s", tc)
	}
}

// TestAnsiStripsNonSGR: cursor moves (CSI other than m) and OSC titles are
// stripped, never rendered — no raw escape reaches the DOM.
func TestAnsiStripsNonSGR(t *testing.T) {
	html := evalUI(t, "ansiToHtml('a\\x1b[2Kb\\x1b]0;title\\x07c\\x1b[1A d')")
	if strings.ContainsRune(html, '\x1b') {
		t.Errorf("raw escape survived:\n%q", html)
	}
	if !strings.Contains(html, "a") || !strings.Contains(html, "b") || !strings.Contains(html, "c") {
		t.Errorf("printable text dropped by stripping:\n%q", html)
	}
}

// TestAnsiEscapesFirst: captured output is attacker-controlled, so it is
// HTML-escaped before SGR spans are added.
func TestAnsiEscapesFirst(t *testing.T) {
	html := evalUI(t, "ansiToHtml('<script>alert(1)</script>\\x1b[31mx\\x1b[0m')")
	if strings.Contains(html, "<script>") {
		t.Errorf("ansi did not escape attacker markup:\n%s", html)
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("expected escaped markup:\n%s", html)
	}
}
