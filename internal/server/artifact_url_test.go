package server

import "testing"

// TestArtifactURL drives the U4 path encoder: each path segment is
// percent-encoded so a file whose name contains #, ?, %, or a space is still
// reachable, while the / separators between segments are preserved.
func TestArtifactURL(t *testing.T) {
	cases := map[string]string{
		"plain.md":            "plain.md",
		"a b/c#d.diff":        "a%20b/c%23d.diff",
		"q?x/y%z":             "q%3Fx/y%25z",
		"artifacts/changes.d": "artifacts/changes.d",
	}
	for in, want := range cases {
		if got := evalUI(t, "artifactURL("+jsStr(in)+")"); got != want {
			t.Errorf("artifactURL(%q) = %q, want %q", in, got, want)
		}
	}
}
