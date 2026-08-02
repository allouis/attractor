package server

import (
	"strings"
	"testing"
)

// TestRunOrigin: a run's origin (web-ui-v2-spec U7 "started-by" provenance)
// is derived from what it was launched from — an item dispatch, a workflow
// run, or a raw manual submission. There is no user identity in the daemon
// (tailnet = trust, no roles), so origin is the honest substitute. An
// item-dispatched run also carries a workflow, so item wins.
func TestRunOrigin(t *testing.T) {
	cases := []struct{ expr, want string }{
		{`runOrigin({item_ref:'gh#1', workflow_name:'w'})`, "item"},
		{`runOrigin({workflow_name:'w'})`, "workflow"},
		{`runOrigin({})`, "manual"},
	}
	for _, c := range cases {
		if got := evalUI(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestRunRepo: the repo label prefers the dispatched owner/name, falling
// back to the cwd's last path segment (a raw run against a checkout still
// shows *something*), and is empty when neither is known.
func TestRunRepo(t *testing.T) {
	cases := []struct{ expr, want string }{
		{`runRepo({repo:'owner/x', cwd:'/h/proj'})`, "owner/x"},
		{`runRepo({cwd:'/home/me/proj'})`, "proj"},
		{`runRepo({cwd:'/home/me/proj/'})`, "proj"},
		{`runRepo({})`, ""},
	}
	for _, c := range cases {
		if got := evalUI(t, c.expr); got != c.want {
			t.Errorf("%s = %q, want %q", c.expr, got, c.want)
		}
	}
}

// TestRunRepoCell: the fleet cell renders the repo label escaped, an empty
// repo as the muted placeholder, and neutralises attacker-controlled repo /
// cwd values (both flow from external items / DOT source).
func TestRunRepoCell(t *testing.T) {
	if got := evalUI(t, `runRepoCell({repo:'owner/x'})`); !strings.Contains(got, "owner/x") {
		t.Errorf("repo cell missing label: %s", got)
	}
	if got := evalUI(t, `runRepoCell({})`); !strings.Contains(got, "muted") {
		t.Errorf("empty repo cell should be the muted placeholder: %s", got)
	}
	xss := evalUI(t, `runRepoCell({repo:'<img src=x onerror=alert(1)>'})`)
	if strings.Contains(xss, "<img src=x onerror=") {
		t.Errorf("repo cell rendered a value as live markup: %s", xss)
	}
}

// TestRunOriginCell: the origin cell carries its origin-<kind> class so it
// can be styled, and the derived word.
func TestRunOriginCell(t *testing.T) {
	got := evalUI(t, `runOriginCell({item_ref:'gh#1'})`)
	if !strings.Contains(got, "origin-item") || !strings.Contains(got, "item") {
		t.Errorf("origin cell missing class/word: %s", got)
	}
}
