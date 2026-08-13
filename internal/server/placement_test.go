package server

import (
	"strings"
	"testing"
)

// placementLaunchers is the registry the resolution tests validate against:
// direct/local plus a vm launcher backed by a two-image registry.
func placementLaunchers() map[string]Launcher {
	vm := NewVMLauncherWithImages(map[string]string{"default": "/a", "node-ts": "/b"}, "default", "")
	return map[string]Launcher{
		"direct": directLauncher{},
		"local":  NewLocalLauncher(),
		"vm":     vm,
	}
}

func TestResolvePlacement(t *testing.T) {
	launchers := placementLaunchers()
	cases := []struct {
		name          string
		sub, repo     placementReq
		defaultRunner string
		wantRunner    string
		wantImage     string
	}{
		{
			name: "submission runner wins over repo and default",
			sub:  placementReq{runner: "vm"}, repo: placementReq{runner: "local"},
			defaultRunner: "direct", wantRunner: "vm",
		},
		{
			name: "repo runner applies when submission is silent",
			repo: placementReq{runner: "vm"}, defaultRunner: "direct", wantRunner: "vm",
		},
		{
			name:          "daemon default applies when neither declares one",
			defaultRunner: "direct", wantRunner: "direct",
		},
		{
			name: "submission image wins over repo image",
			sub:  placementReq{runner: "vm", image: "node-ts"}, repo: placementReq{image: "default"},
			defaultRunner: "direct", wantRunner: "vm", wantImage: "node-ts",
		},
		{
			name: "repo image applies when submission is silent",
			sub:  placementReq{runner: "vm"}, repo: placementReq{image: "node-ts"},
			defaultRunner: "direct", wantRunner: "vm", wantImage: "node-ts",
		},
		{
			name: "repo declares only an image, default runner is vm",
			repo: placementReq{image: "node-ts"}, defaultRunner: "vm",
			wantRunner: "vm", wantImage: "node-ts",
		},
		{
			name:          "a non-vm runner drops the image (irrelevant)",
			sub:           placementReq{runner: "direct", image: "node-ts"},
			defaultRunner: "direct", wantRunner: "direct", wantImage: "",
		},
		{
			name: "empty image on a vm run resolves to the launcher default",
			sub:  placementReq{runner: "vm"}, defaultRunner: "direct", wantRunner: "vm", wantImage: "",
		},
		{
			name:          "a runner the daemon defaults to is trusted, not validated",
			defaultRunner: "custom", wantRunner: "custom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolvePlacement(tc.sub, tc.repo, tc.defaultRunner, launchers)
			if err != nil {
				t.Fatalf("resolvePlacement: %v", err)
			}
			if got.runner != tc.wantRunner {
				t.Errorf("runner = %q, want %q", got.runner, tc.wantRunner)
			}
			if got.image != tc.wantImage {
				t.Errorf("image = %q, want %q", got.image, tc.wantImage)
			}
		})
	}
}

func TestResolvePlacementRejectsUnknownRunner(t *testing.T) {
	launchers := placementLaunchers()
	for _, tc := range []struct {
		name      string
		sub, repo placementReq
	}{
		{name: "from submission", sub: placementReq{runner: "bogus"}},
		{name: "from repo config", repo: placementReq{runner: "bogus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolvePlacement(tc.sub, tc.repo, "direct", launchers)
			if err == nil {
				t.Fatal("expected an unknown-runner rejection")
			}
			for _, want := range []string{"bogus", "direct", "local", "vm"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

func TestResolvePlacementRejectsUnknownImage(t *testing.T) {
	launchers := placementLaunchers()
	_, err := resolvePlacement(placementReq{runner: "vm", image: "bogus"}, placementReq{}, "direct", launchers)
	if err == nil {
		t.Fatal("expected an unknown-image rejection")
	}
	for _, want := range []string{"bogus", "default", "node-ts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}
