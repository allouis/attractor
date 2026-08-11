package server

import (
	"strings"
	"testing"
)

// TestRunHeadTitle: the R1 header card's issue line links the identifier to the
// tracker url and appends the title as plain text (ui-run-view-v3 R1), matching
// the approved mock ("<a>HKG-1914</a> · Spike …").
func TestRunHeadTitle(t *testing.T) {
	html := evalUI(t, `runHeadTitle({id:"d3199c0d93eddde5",item:{identifier:"HKG-1914",title:"Spike the jobs backend",url:"https://linear.app/x/HKG-1914"}})`)
	if !strings.Contains(html, `href="https://linear.app/x/HKG-1914"`) {
		t.Errorf("identifier not linked to the tracker url:\n%s", html)
	}
	if !strings.Contains(html, ">HKG-1914</a>") {
		t.Errorf("identifier is not the link text:\n%s", html)
	}
	if !strings.Contains(html, "Spike the jobs backend") {
		t.Errorf("title missing:\n%s", html)
	}
}

// TestRunHeadTitleFallback: no item falls back to the run id, no title falls
// back to the identifier alone.
func TestRunHeadTitleFallback(t *testing.T) {
	id := evalUI(t, `runHeadTitle({id:"abc123",item:null})`)
	if !strings.Contains(id, "abc123") {
		t.Errorf("no-item header should show the run id:\n%s", id)
	}
	identOnly := evalUI(t, `runHeadTitle({id:"abc123",item:{identifier:"HKG-1",url:"http://x"}})`)
	if !strings.Contains(identOnly, ">HKG-1</a>") || strings.Contains(identOnly, " · ") {
		t.Errorf("identifier-only header wrong:\n%s", identOnly)
	}
}

// TestRunHeadTitleEscapes: item metadata is attacker-controlled (external
// tracker) and must not render as live markup.
func TestRunHeadTitleEscapes(t *testing.T) {
	html := evalUI(t, `runHeadTitle({item:{identifier:"<img src=x onerror=alert(1)>",title:"</a><script>alert(2)</script>",url:"javascript:x\"><b>"}})`)
	for _, bad := range []string{"<img src=x onerror=", "<script>alert(2)</script>", `"><b>`} {
		if strings.Contains(html, bad) {
			t.Errorf("runHeadTitle rendered attacker input as markup (%q):\n%s", bad, html)
		}
	}
}

// TestFmtElapsed pins the header's live clock formatting: hours past an hour,
// minutes+seconds past a minute, seconds under a minute.
func TestFmtElapsed(t *testing.T) {
	cases := []struct{ start, end, want string }{
		{"2026-08-11T12:00:00Z", "2026-08-11T13:47:30Z", "1h 47m"},
		{"2026-08-11T12:00:00Z", "2026-08-11T12:07:07Z", "7m 07s"},
		{"2026-08-11T12:00:00Z", "2026-08-11T12:00:09Z", "9s"},
	}
	for _, c := range cases {
		got := evalUI(t, `fmtElapsed("`+c.start+`","`+c.end+`")`)
		if got != c.want {
			t.Errorf("fmtElapsed(%q,%q) = %q, want %q", c.start, c.end, got, c.want)
		}
	}
	if got := evalUI(t, `fmtElapsed("")`); got != "—" {
		t.Errorf("fmtElapsed(empty) = %q, want em-dash", got)
	}
}
