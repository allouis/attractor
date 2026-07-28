package attractor_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

const gateDOT = `digraph g {
	start [shape=Mdiamond]
	gate [shape=hexagon, label="Approve?"]
	work [prompt="x"]
	done [shape=Msquare]
	start -> gate
	gate -> work [label="Yes"]
	gate -> done [label="No"]
	work -> done
}`

func newQueueServer(t *testing.T, max int) (*server.Server, string) {
	t.Helper()
	logsRoot := t.TempDir()
	srv := server.New(server.Config{
		Addr:              "127.0.0.1:0",
		LogsRoot:          logsRoot,
		MakeHandlers:      server.DefaultHandlers(handler.Codergen{}),
		MaxConcurrentRuns: max,
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })
	return srv, logsRoot
}

func getRunStatus(t *testing.T, srv *server.Server, id string) string {
	t.Helper()
	resp, err := http.Get(srv.URL() + "/pipelines/" + id)
	must(t, err)
	defer resp.Body.Close()
	var summary struct {
		Status string `json:"status"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&summary))
	return summary.Status
}

// answerGate waits for a run's gate question to register, then answers it.
func answerGate(t *testing.T, srv *server.Server, id, key, label string) {
	t.Helper()
	var qid string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if qs := getQuestions(t, srv, id); len(qs) > 0 {
			qid = qs[0].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if qid == "" {
		t.Fatalf("gate question for %s never registered", id)
	}
	body, _ := json.Marshal(server.AnswerPayload{Key: key, Label: label})
	url := fmt.Sprintf("%s/pipelines/%s/questions/%s/answer", srv.URL(), id, qid)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	must(t, err)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("answer %s status=%d", id, resp.StatusCode)
	}
}

// TestServer_QueueLimitAndFIFO drives three gate-blocked runs through a
// single execution slot and confirms they are admitted one at a time in
// submission order (service-spec §3).
func TestServer_QueueLimitAndFIFO(t *testing.T) {
	srv, _ := newQueueServer(t, 1)
	a := submitPipeline(t, srv, gateDOT)
	b := submitPipeline(t, srv, gateDOT)
	c := submitPipeline(t, srv, gateDOT)

	// A holds the only slot; B and C wait their turn.
	waitForRunStatus(t, srv, a, "running")
	time.Sleep(50 * time.Millisecond)
	if s := getRunStatus(t, srv, b); s != "queued" {
		t.Fatalf("B should be queued while A runs, got %q", s)
	}
	if s := getRunStatus(t, srv, c); s != "queued" {
		t.Fatalf("C should be queued while A runs, got %q", s)
	}

	// Finish A; the next in line (B) is admitted, C keeps waiting.
	answerGate(t, srv, a, "Y", "Yes")
	waitForRunStatus(t, srv, a, "completed")
	waitForRunStatus(t, srv, b, "running")
	time.Sleep(50 * time.Millisecond)
	if s := getRunStatus(t, srv, c); s != "queued" {
		t.Fatalf("C should still be queued while B runs, got %q", s)
	}

	// Finish B; C is admitted last.
	answerGate(t, srv, b, "Y", "Yes")
	waitForRunStatus(t, srv, b, "completed")
	waitForRunStatus(t, srv, c, "running")
	answerGate(t, srv, c, "Y", "Yes")
	waitForRunStatus(t, srv, c, "completed")
}

// TestServer_CancelQueuedRunNeverStarts confirms cancelling a run while
// it sits in the queue drops it without ever executing.
func TestServer_CancelQueuedRunNeverStarts(t *testing.T) {
	srv, logsRoot := newQueueServer(t, 1)
	a := submitPipeline(t, srv, gateDOT)
	b := submitPipeline(t, srv, gateDOT)
	waitForRunStatus(t, srv, a, "running")
	if s := getRunStatus(t, srv, b); s != "queued" {
		t.Fatalf("B should be queued, got %q", s)
	}

	resp, err := http.Post(srv.URL()+"/pipelines/"+b+"/cancel", "application/json", nil)
	must(t, err)
	resp.Body.Close()

	// Release A and let the dispatcher drain.
	answerGate(t, srv, a, "Y", "Yes")
	waitForRunStatus(t, srv, a, "completed")
	waitForRunStatus(t, srv, b, "cancelled")

	// A cancelled-while-queued run never ran, so the engine wrote no
	// events.jsonl for it.
	if _, err := os.Stat(filepath.Join(logsRoot, b, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("cancelled queued run should not have executed (events.jsonl err=%v)", err)
	}
}
