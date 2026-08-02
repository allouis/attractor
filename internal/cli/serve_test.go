package cli

import (
	"reflect"
	"testing"

	"github.com/allouis/attractor/internal/config"
)

// TestParseVMImages checks the repeatable --vm-runner flag parses into a
// name -> script map: a bare value registers the "default" image, name=path
// registers a named image, and empty/duplicate names are rejected (VM1).
func TestParseVMImages(t *testing.T) {
	ok := map[string]struct {
		in   []string
		want map[string]string
	}{
		"bare value is default":   {[]string{"/p"}, map[string]string{"default": "/p"}},
		"named images":            {[]string{"node=/a", "python=/b"}, map[string]string{"node": "/a", "python": "/b"}},
		"bare plus named":         {[]string{"/a", "python=/b"}, map[string]string{"default": "/a", "python": "/b"}},
		"path may contain equals": {[]string{"node=/a=b"}, map[string]string{"node": "/a=b"}},
		"empty":                   {nil, map[string]string{}},
	}
	for name, c := range ok {
		t.Run(name, func(t *testing.T) {
			got, err := parseVMImages(c.in)
			if err != nil {
				t.Fatalf("parseVMImages(%v): %v", c.in, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseVMImages(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}

	bad := map[string][]string{
		"empty name":     {"=/x"},
		"duplicate name": {"node=/a", "node=/b"},
		"duplicate bare": {"/a", "/b"},
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := parseVMImages(in); err == nil {
				t.Errorf("parseVMImages(%v) = nil error, want error", in)
			}
		})
	}
}

// TestServeReposLoadsConfig checks serve wires the central config.json
// through to items.Repos: an owner/name → path entry is projected and
// looked up, backing POST /items/run's repo → cwd resolution (items-spec
// I3/I4).
func TestServeReposLoadsConfig(t *testing.T) {
	home := t.TempDir()
	doc := config.Document{Repos: map[string]config.RepoConfig{
		"allouis/attractor": {Path: "/home/agent/attractor"},
	}}
	if err := doc.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}

	repos, err := serveRepos(home)
	if err != nil {
		t.Fatalf("serveRepos: %v", err)
	}
	if got, ok := repos.Path("allouis/attractor"); !ok || got != "/home/agent/attractor" {
		t.Errorf("repos.Path = %q, ok=%v; want /home/agent/attractor", got, ok)
	}
}
