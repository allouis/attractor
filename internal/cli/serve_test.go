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
		"empty name":       {"=/x"},
		"duplicate name":   {"node=/a", "node=/b"},
		"duplicate bare":   {"/a", "/b"},
		"empty path named": {"python="},
		"empty path bare":  {""},
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

// TestResolveLaunchersGatesOnExplicitIntent checks that config-supplied
// vm_images alone do NOT activate the vm launcher: a `--runner local`
// daemon with vm_images in config but no --runner vm / --vm-runner flag
// gets no vm launcher (and never attempts a nix build), so a config edit
// can't silently arm VMs or break serve startup (VM1 prod-safety).
func TestResolveLaunchersGatesOnExplicitIntent(t *testing.T) {
	vmDir := t.TempDir()
	// Config-only images, no CLI intent → no vm launcher, no build, no error.
	_, all, err := resolveLaunchers("local", nil, map[string]string{"python": "/p"}, vmDir)
	if err != nil {
		t.Fatalf("resolveLaunchers: %v", err)
	}
	if _, ok := all["vm"]; ok {
		t.Errorf("vm launcher armed by config presence alone; want it gated on explicit intent")
	}
	// A --vm-runner flag (here with a default supplied so no nix build is
	// needed) is explicit intent → vm launcher present, config merged in.
	_, all, err = resolveLaunchers("local", map[string]string{"default": "/d"}, map[string]string{"python": "/p"}, vmDir)
	if err != nil {
		t.Fatalf("resolveLaunchers (cli intent): %v", err)
	}
	if _, ok := all["vm"]; !ok {
		t.Errorf("vm launcher not built despite --vm-runner intent")
	}
	// The retired in-process runner is rejected.
	if _, _, err := resolveLaunchers("direct", nil, nil, vmDir); err == nil {
		t.Error("--runner direct accepted; the in-process launcher is retired")
	}
}

// TestServeVMImagesLoadsConfig checks serve projects config.json vm_images
// into the VM boot-image registry, returning a fresh map serve can overlay
// --vm-runner flags onto without mutating the loaded document (VM1).
func TestServeVMImagesLoadsConfig(t *testing.T) {
	home := t.TempDir()
	doc := config.Document{VMImages: map[string]string{"default": ".#vm-runner", "python": "/p"}}
	if err := doc.Save(home); err != nil {
		t.Fatalf("Save: %v", err)
	}
	images, err := serveVMImages(home)
	if err != nil {
		t.Fatalf("serveVMImages: %v", err)
	}
	want := map[string]string{"default": ".#vm-runner", "python": "/p"}
	if !reflect.DeepEqual(images, want) {
		t.Errorf("serveVMImages = %v, want %v", images, want)
	}
	// A missing config.json is not an error: no images.
	empty, err := serveVMImages(t.TempDir())
	if err != nil || len(empty) != 0 {
		t.Errorf("serveVMImages(empty home) = %v, err=%v; want empty, nil", empty, err)
	}

	// A config image with an empty path fails loudly at startup, not
	// cryptically at VM boot (VM1 design goal).
	badHome := t.TempDir()
	badDoc := config.Document{VMImages: map[string]string{"python": ""}}
	if err := badDoc.Save(badHome); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := serveVMImages(badHome); err == nil {
		t.Errorf("serveVMImages accepted an empty image path; want error")
	}
}
