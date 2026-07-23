package attractor_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/handler"
	"github.com/fabro/attractor/internal/server"
)

// TestServer_Artifacts confirms GET /pipelines/{id}/artifacts/{path...}
// serves a run's on-disk stage files (service-spec §4 node pane, which
// links to prompt.md / response.md / tool_calls/).
func TestServer_Artifacts(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		start [shape=Mdiamond]
		work [prompt="hello prompt"]
		done [shape=Msquare]
		start -> work -> done
	}`
	id := submitPipeline(t, srv, dot)
	waitForRunStatus(t, srv, id, "completed")

	// The codergen handler writes prompt.md/response.md per node.
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/artifacts/work/prompt.md")
	must(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prompt.md status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "hello prompt") {
		t.Fatalf("prompt.md missing prompt text: %q", body)
	}

	resp, err = http.Get(srv.URL() + "/pipelines/" + id + "/artifacts/work/response.md")
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("response.md status=%d", resp.StatusCode)
	}

	// Path traversal must not escape the run's logs directory.
	resp, err = http.Get(srv.URL() + "/pipelines/" + id + "/artifacts/../../../etc/passwd")
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("traversal leaked file: status=%d", resp.StatusCode)
	}

	// Unknown run id → 404.
	resp, err = http.Get(srv.URL() + "/pipelines/deadbeef/artifacts/work/prompt.md")
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run status=%d, want 404", resp.StatusCode)
	}
}
