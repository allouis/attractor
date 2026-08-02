package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_UI_ConfigTab guards the Config view shell (config-screen-spec
// C4): a fourth nav tab over the same hash router, with a mount point the
// hand-built panels render into. The panel behaviour (repos/linear/
// providers render + save) is JS runtime, verified separately via the vm
// harness in internal/server.
func TestServer_UI_ConfigTab(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	for _, marker := range []string{`href="#config"`, `id="nav-config"`, `id="config-view"`} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing Config-view marker %q", marker)
		}
	}
}

// TestServer_UI_ConfigDirtyGuard guards the unsaved-changes guard wiring
// (config-screen-spec C5 dirty state): leaving the Config view — by nav or a
// full page unload — with pending edits must warn rather than silently
// discard them. The guard is DOM/navigation behaviour (not exercisable by
// the vm harness), so this asserts the wiring is served: the beforeunload
// listener and the in-app leave prompt.
func TestServer_UI_ConfigDirtyGuard(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	resp, err := http.Get(srv.URL() + "/ui")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	for _, marker := range []string{"beforeunload", "Discard unsaved config changes"} {
		if !strings.Contains(page, marker) {
			t.Errorf("page missing dirty-guard wiring %q", marker)
		}
	}
}
