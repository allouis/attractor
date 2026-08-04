package server

import (
	"strings"
	"testing"
)

// The Runs list and the two Config tables share one shell (dataTable). T6b
// adopts the Tailwind Plus table (docs/tailwind-components/table.html),
// token-remapped: an overflow-safe wrapper, divide-rule rows and a semibold
// ink header — layered over the T2/T3 responsive collapse so the ≤640px
// stacked cards survive (ui-tailwind-spec T6b). These drive the real render
// fns through the node vm sandbox (evalUI).
var t6bTableCases = []struct{ name, expr string }{
	{"runs", `runsTableHtml([{id:"deadbeefcafe",status:"running",started_at:"2026-07-25"}])`},
	{"repos", `reposPanelHtml({"a/b":{path:"/x",checks:{}}})`},
	{"providers", `providersPanelHtml({default_provider:"",providers:{p:{backend:"acp"}}})`},
}

// TestTablesAdoptTailwindPlusShell: each list adopts the Tailwind Plus table
// shell and a token-remapped strong header, without leaking stock palette.
func TestTablesAdoptTailwindPlusShell(t *testing.T) {
	for _, tc := range t6bTableCases {
		html := evalUI(t, tc.expr)
		// Tailwind Plus table shell: overflow-safe wrapper, min-width, divide rules.
		for _, want := range []string{"overflow-x-auto", "min-w-full", "divide-y", "divide-line"} {
			if !strings.Contains(html, want) {
				t.Errorf("%s table missing Tailwind Plus shell class %q:\n%s", tc.name, want, html)
			}
		}
		// Strong, theme-aware header — the remap of gray-900 font-semibold.
		if !strings.Contains(html, "text-ink") || !strings.Contains(html, "font-semibold") {
			t.Errorf("%s table header not token-remapped (text-ink/font-semibold):\n%s", tc.name, html)
		}
		// No stock Tailwind palette leaks past the token remap.
		for _, bad := range []string{"gray-", "indigo-"} {
			if strings.Contains(html, bad) {
				t.Errorf("%s table leaked a hardcoded palette %q:\n%s", tc.name, bad, html)
			}
		}
	}
}

// TestTablesKeepStackedCards: the T2/T3 stacked-card collapse survives the
// Tailwind Plus adoption — each list still renders block→table with a bordered
// card row so a 390px phone has no clipped columns.
func TestTablesKeepStackedCards(t *testing.T) {
	for _, tc := range t6bTableCases {
		html := evalUI(t, tc.expr)
		for _, want := range []string{"block", "sm:table", "sm:table-header-group", "sm:table-row", "border-line", "bg-surface-1"} {
			if !strings.Contains(html, want) {
				t.Errorf("%s table lost the responsive stacked-card class %q:\n%s", tc.name, want, html)
			}
		}
	}
}
