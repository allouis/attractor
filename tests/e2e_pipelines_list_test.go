package attractor_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestServer_ListPipelines confirms GET /pipelines returns registry
// summaries — id, status, started, pipeline name, cwd — newest first
// (service-spec §3).
func TestServer_ListPipelines(t *testing.T) {
	repo := t.TempDir()
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph named {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	id1 := submitPipeline(t, srv, dot)
	id2 := submitJSON(t, srv, map[string]any{"dot": dot, "cwd": repo})
	waitForRunStatus(t, srv, id1, "completed")
	waitForRunStatus(t, srv, id2, "completed")

	resp, err := http.Get(srv.URL() + "/pipelines")
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d", resp.StatusCode)
	}
	var payload struct {
		Pipelines []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			StartedAt string `json:"started_at"`
			GraphName string `json:"graph_name"`
			Cwd       string `json:"cwd"`
		} `json:"pipelines"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&payload))

	byID := map[string]int{}
	for i, p := range payload.Pipelines {
		byID[p.ID] = i
	}
	i1, ok1 := byID[id1]
	i2, ok2 := byID[id2]
	if !ok1 || !ok2 {
		t.Fatalf("list missing runs: got %+v", payload.Pipelines)
	}
	// Newest first: id2 was submitted after id1.
	if i2 > i1 {
		t.Fatalf("expected newest-first order; id2 index %d should precede id1 index %d", i2, i1)
	}
	p1 := payload.Pipelines[i1]
	if p1.Status != "completed" || p1.GraphName != "named" || p1.StartedAt == "" {
		t.Fatalf("run1 summary wrong: %+v", p1)
	}
	if got := payload.Pipelines[i2].Cwd; got != repo {
		t.Fatalf("run2 cwd = %q, want %q", got, repo)
	}
}

// TestServer_ListPipelinesReportsUsage confirms the per-run token
// rollup surfaces in GET /pipelines summaries (service-spec §6).
func TestServer_ListPipelinesReportsUsage(t *testing.T) {
	be := handler.Codergen{Backend: backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		env.Emit(engine.Event{
			Kind:   engine.EventUsage,
			NodeID: env.Node.ID,
			Usage:  &engine.Usage{InputTokens: 100, OutputTokens: 20},
		})
		return backend.Result{ResponseText: "ok"}, nil
	})}
	srv := newTestServer(t, server.DefaultHandlers(be))
	dot := `digraph named {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	id := submitPipeline(t, srv, dot)
	waitForRunStatus(t, srv, id, "completed")

	resp, err := http.Get(srv.URL() + "/pipelines")
	must(t, err)
	defer resp.Body.Close()
	var payload struct {
		Pipelines []struct {
			ID     string `json:"id"`
			Tokens *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"tokens"`
		} `json:"pipelines"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&payload))

	var tokens *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	for _, p := range payload.Pipelines {
		if p.ID == id {
			tokens = p.Tokens
		}
	}
	if tokens == nil {
		t.Fatalf("run summary missing token rollup: %+v", payload.Pipelines)
	}
	// Single codergen stage "work".
	if tokens.InputTokens != 100 || tokens.OutputTokens != 20 {
		t.Fatalf("token rollup wrong: %+v", tokens)
	}
}
