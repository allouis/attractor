package server

import (
	"strings"
	"testing"
)

// TestFmtDuration drives the UI's stage-timing formatter (web-ui-v2-spec U2 —
// the inspector shows a stage's elapsed time). Sub-minute reads as "N.Ns";
// a minute or more as "Nm SSs"; a missing end is a running stage; a missing
// start is the em-dash placeholder shared with fmtDate.
func TestFmtDuration(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{`fmtDuration('2026-08-02T10:00:00Z','2026-08-02T10:00:01.200Z')`, "1.2s"},
		{`fmtDuration('2026-08-02T10:00:00Z','2026-08-02T10:03:04Z')`, "3m 04s"},
		{`fmtDuration('2026-08-02T10:00:00Z','')`, "running"},
		{`fmtDuration('','')`, "—"},
	}
	for _, c := range cases {
		got := evalUI(t, c.expr)
		if !strings.Contains(got, c.want) {
			t.Errorf("%s = %q, want substring %q", c.expr, got, c.want)
		}
	}
}
