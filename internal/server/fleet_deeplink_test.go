package server

import "testing"

// TestRunsFilterFromHash: the Runs hash carries the filter state so a
// narrowed fleet is shareable/bookmarkable (web-ui-v2-spec U7 deep links).
// Keys decode into the runMatchesFilter shape; the text field rides on `q`;
// absent keys default to empty; values are percent-decoded.
func TestRunsFilterFromHash(t *testing.T) {
	cases := []struct{ expr, want string }{
		{`runsFilterFromHash('#runs?status=failed&repo=owner%2Fx&q=foo').status`, "failed"},
		{`runsFilterFromHash('#runs?status=failed&repo=owner%2Fx&q=foo').repo`, "owner/x"},
		{`runsFilterFromHash('#runs?status=failed&repo=owner%2Fx&q=foo').text`, "foo"},
		{`runsFilterFromHash('#runs?workflow=review&origin=item').workflow`, "review"},
		{`runsFilterFromHash('#runs?workflow=review&origin=item').origin`, "item"},
		{`runsFilterFromHash('#runs').status`, ""},
		{`runsFilterFromHash('#runs').text`, ""},
	}
	for _, c := range cases {
		if got := evalUI(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestRunsFilterToHash: serialise the filter back to a hash — non-empty keys
// only, a stable order, percent-encoded, text as `q`. An empty filter is the
// bare `runs` hash (no query).
func TestRunsFilterToHash(t *testing.T) {
	cases := []struct{ expr, want string }{
		{`runsFilterToHash({status:'failed', repo:'owner/x', text:'foo'})`, "runs?status=failed&repo=owner%2Fx&q=foo"},
		{`runsFilterToHash({workflow:'review', origin:'item'})`, "runs?workflow=review&origin=item"},
		{`runsFilterToHash({})`, "runs"},
	}
	for _, c := range cases {
		if got := evalUI(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestRunsFilterRoundTrip: from→to is stable, so re-serialising a parsed hash
// reproduces it (the toolbar writes the hash it just read on no change).
func TestRunsFilterRoundTrip(t *testing.T) {
	expr := `runsFilterToHash(runsFilterFromHash('#runs?status=failed&workflow=review&repo=owner%2Fx&origin=item&q=foo'))`
	want := "runs?status=failed&workflow=review&repo=owner%2Fx&origin=item&q=foo"
	if got := evalUI(t, expr); got != want {
		t.Errorf("round-trip = %q, want %q", got, want)
	}
}
