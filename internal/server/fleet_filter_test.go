package server

import "testing"

// TestRunMatchesFilter: the fleet toolbar narrows the polled fleet
// client-side (web-ui-v2-spec U7), mirroring eventMatchesFilter. An empty
// filter matches everything; status/workflow/repo/origin are exact; the
// needs-human pseudo-status keys the run's live-blocked flag (not a status);
// text matches case-insensitively across id, item, workflow and repo.
func TestRunMatchesFilter(t *testing.T) {
	// A running run dispatched from an item against owner/x.
	p := `{id:'abc123', status:'running', item_ref:'gh#7', workflow_name:'review', repo:'owner/x'}`
	blocked := `{id:'d4', status:'running', needs_human:true, workflow_name:'w'}`
	cases := []struct {
		expr string
		want string
	}{
		{`runMatchesFilter(` + p + `, {})`, "true"},
		{`runMatchesFilter(` + p + `, {status:'running'})`, "true"},
		{`runMatchesFilter(` + p + `, {status:'failed'})`, "false"},
		{`runMatchesFilter(` + blocked + `, {status:'needs-human'})`, "true"},
		{`runMatchesFilter(` + p + `, {status:'needs-human'})`, "false"},
		{`runMatchesFilter(` + p + `, {workflow:'review'})`, "true"},
		{`runMatchesFilter(` + p + `, {workflow:'other'})`, "false"},
		{`runMatchesFilter(` + p + `, {repo:'owner/x'})`, "true"},
		{`runMatchesFilter(` + p + `, {repo:'owner/y'})`, "false"},
		{`runMatchesFilter(` + p + `, {origin:'item'})`, "true"},
		{`runMatchesFilter(` + p + `, {origin:'workflow'})`, "false"},
		{`runMatchesFilter(` + p + `, {text:'REVIEW'})`, "true"},  // workflow, case-insensitive
		{`runMatchesFilter(` + p + `, {text:'gh#7'})`, "true"},    // item ref
		{`runMatchesFilter(` + p + `, {text:'owner/x'})`, "true"}, // repo
		{`runMatchesFilter(` + p + `, {text:'abc'})`, "true"},     // id
		{`runMatchesFilter(` + p + `, {text:'nomatch'})`, "false"},
		{`runMatchesFilter(` + p + `, {status:'running', text:'nomatch'})`, "false"}, // AND
	}
	for _, c := range cases {
		if got := evalUI(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}
