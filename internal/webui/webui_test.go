package webui

import (
	"bytes"
	"testing"
)

func TestPagesBundleLocalAssets(t *testing.T) {
	for name, page := range map[string][]byte{"hub": HubIndex, "run": Waterfall} {
		t.Run(name, func(t *testing.T) {
			for _, marker := range [][]byte{[]byte("/* UI_CSS */"), []byte("/* UI_CORE */"), []byte("/* UI_APP */")} {
				if bytes.Contains(page, marker) {
					t.Fatalf("unexpanded UI asset %q", marker)
				}
			}
			if !bytes.Contains(page, []byte(`id="ui-core"`)) || !bytes.Contains(page, []byte("globalThis.UI")) {
				t.Fatal("UI core missing from bundled page")
			}
			if !bytes.Contains(page, []byte("--surface:")) {
				t.Fatal("dashboard styles missing from bundled page")
			}
		})
	}
}
