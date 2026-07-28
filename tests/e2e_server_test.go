package attractor_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

func newTestServer(t *testing.T, factory server.HandlerFactory) *server.Server {
	t.Helper()
	srv := server.New(server.Config{
		Addr:         "127.0.0.1:0",
		LogsRoot:     t.TempDir(),
		MakeHandlers: factory,
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func TestServer_SubmitAndGet(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	resp, err := http.Post(srv.URL()+"/pipelines", "text/vnd.graphviz", strings.NewReader(dot))
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit status=%d body=%s", resp.StatusCode, body)
	}
	var payload struct{ ID string }
	must(t, json.NewDecoder(resp.Body).Decode(&payload))
	if payload.ID == "" {
		t.Fatal("expected pipeline id")
	}
	waitForRunStatus(t, srv, payload.ID, "completed")
}

func TestServer_SSEStreamsEvents(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	id := submitPipeline(t, srv, dot)
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/events")
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status=%d", resp.StatusCode)
	}
	// Read SSE events until pipeline_completed or timeout.
	scanner := bufio.NewScanner(resp.Body)
	deadline := time.After(3 * time.Second)
	gotStart, gotEnd := false, false
	for !gotEnd {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for SSE events; gotStart=%v gotEnd=%v", gotStart, gotEnd)
		default:
		}
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			ek := strings.TrimPrefix(line, "event: ")
			if ek == string(engine.EventPipelineStarted) {
				gotStart = true
			}
			if ek == string(engine.EventPipelineCompleted) || ek == string(engine.EventPipelineFailed) {
				gotEnd = true
			}
		}
	}
	if !gotStart || !gotEnd {
		t.Fatalf("missing lifecycle events: start=%v end=%v", gotStart, gotEnd)
	}
}

func TestServer_RemoteHumanGate(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		start [shape=Mdiamond]
		gate [shape=hexagon, label="Approve?"]
		yes [prompt="proceed"]
		no [prompt="halt"]
		done [shape=Msquare]
		start -> gate
		gate -> yes [label="Yes"]
		gate -> no [label="No"]
		yes -> done
		no -> done
	}`
	id := submitPipeline(t, srv, dot)

	// Wait for the gate question to appear.
	var qid string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		qs := getQuestions(t, srv, id)
		if len(qs) > 0 {
			qid = qs[0].ID
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if qid == "" {
		t.Fatal("question never registered")
	}

	// Submit answer choosing "Yes".
	body, _ := json.Marshal(server.AnswerPayload{Key: "Y", Label: "Yes"})
	answerURL := fmt.Sprintf("%s/pipelines/%s/questions/%s/answer", srv.URL(), id, qid)
	resp, err := http.Post(answerURL, "application/json", bytes.NewReader(body))
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("answer status=%d", resp.StatusCode)
	}
	waitForRunStatus(t, srv, id, "completed")
}

func TestServer_GraphEndpointReturnsSVG(t *testing.T) {
	if _, err := exec.LookPath("dot"); err != nil {
		t.Skip("graphviz `dot` not on PATH")
	}
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	id := submitPipeline(t, srv, `digraph p {
		start [shape=Mdiamond]
		a [prompt="x"]
		done [shape=Msquare]
		start -> a -> done
	}`)
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/graph")
	must(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("content-type=%q", resp.Header.Get("Content-Type"))
	}
	if !bytes.Contains(body, []byte("<svg")) {
		t.Fatalf("graph endpoint did not return SVG")
	}
}

func TestServer_CheckpointEndpoint(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	id := submitPipeline(t, srv, `digraph p {
		start [shape=Mdiamond]
		a [prompt="x"]
		b [prompt="y"]
		done [shape=Msquare]
		start -> a -> b -> done
	}`)
	waitForRunStatus(t, srv, id, "completed")
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/checkpoint")
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("checkpoint status=%d", resp.StatusCode)
	}
	var ck struct {
		CompletedNodes []string `json:"completed_nodes"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&ck))
	if len(ck.CompletedNodes) < 2 {
		t.Fatalf("checkpoint missing completed nodes: %+v", ck)
	}
}

// ---------- helpers ----------

func submitPipeline(t *testing.T, srv *server.Server, dot string) string {
	t.Helper()
	resp, err := http.Post(srv.URL()+"/pipelines", "text/vnd.graphviz", strings.NewReader(dot))
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("submit failed: %d %s", resp.StatusCode, body)
	}
	var payload struct{ ID string }
	must(t, json.NewDecoder(resp.Body).Decode(&payload))
	return payload.ID
}

type questionView struct {
	ID     string `json:"id"`
	NodeID string `json:"node_id"`
	Text   string `json:"text"`
}

func getQuestions(t *testing.T, srv *server.Server, id string) []questionView {
	t.Helper()
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/questions")
	must(t, err)
	defer resp.Body.Close()
	var payload struct {
		Questions []questionView `json:"questions"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&payload))
	return payload.Questions
}

func waitForRunStatus(t *testing.T, srv *server.Server, id, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(srv.URL() + "/pipelines/" + id)
		if err == nil {
			var summary struct {
				Status string `json:"status"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&summary)
			resp.Body.Close()
			if summary.Status == want {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("status never reached %q", want)
}
