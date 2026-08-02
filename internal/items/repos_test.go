package items

import "testing"

// TestReposPath: a registered owner/name resolves to its checkout, a miss
// returns ok=false (items-spec I3). The map is now a projection of the
// central config.json (config-screen-spec); loading lives there.
func TestReposPath(t *testing.T) {
	repos := Repos{"allouis/attractor": "/home/agent/attractor"}

	if got, ok := repos.Path("allouis/attractor"); !ok || got != "/home/agent/attractor" {
		t.Errorf("Path(allouis/attractor) = %q, %v; want %q, true", got, ok, "/home/agent/attractor")
	}
	if got, ok := repos.Path("nope/x"); ok {
		t.Errorf("Path(nope/x) = %q, %v; want \"\", false", got, ok)
	}
}
