package attractor_test

import (
	"bytes"
	"io"
	"net/http"
	"os/exec"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// getGraphEngine fetches a run's graph SVG laid out with the given engine
// (empty = no ?engine=), asserting a 200 SVG, and returns the bytes.
func getGraphEngine(t *testing.T, srv *server.Server, id, engine string) []byte {
	t.Helper()
	url := srv.URL() + "/pipelines/" + id + "/graph"
	if engine != "" {
		url += "?engine=" + engine
	}
	resp, err := http.Get(url)
	must(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("engine %q: status=%d body=%.120s", engine, resp.StatusCode, body)
	}
	if !bytes.Contains(body, []byte("<svg")) {
		t.Fatalf("engine %q: not SVG: %.120s", engine, body)
	}
	return body
}

// TestServer_GraphEngineSelector guards the graph endpoint engine selector
// (ui-tailwind-spec T4b): GET /pipelines/{id}/graph accepts an allowlisted
// ?engine= that picks the graphviz layout algorithm; an unknown engine falls
// back to the default (dot) rather than erroring or reaching exec. A chosen
// engine must actually change the layout (proof the -K flag is plumbed), while
// a bogus engine must render identically to the default (proof it is screened).
func TestServer_GraphEngineSelector(t *testing.T) {
	if _, err := exec.LookPath("dot"); err != nil {
		t.Skip("graphviz `dot` not on PATH")
	}
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	id := submitPipeline(t, srv, `digraph p {
		start [shape=Mdiamond]
		a [prompt="x"]
		b [prompt="y"]
		done [shape=Msquare]
		start -> a -> done
		start -> b -> done
	}`)

	deflt := getGraphEngine(t, srv, id, "")
	neato := getGraphEngine(t, srv, id, "neato")
	bogus := getGraphEngine(t, srv, id, "bogus")

	if bytes.Equal(deflt, neato) {
		t.Errorf("engine=neato produced identical SVG to default: -K<engine> not plumbed")
	}
	if !bytes.Equal(deflt, bogus) {
		t.Errorf("engine=bogus did not fall back to default: allowlist not screened")
	}
}
