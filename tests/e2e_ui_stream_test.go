package attractor_test

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/fabro/attractor/internal/handler"
	"github.com/fabro/attractor/internal/server"
)

// TestServer_StreamCompletedRun guards a deadlock the UI triggers every
// time it opens a finished run: streaming events for a completed run must
// replay history and close cleanly, leaving the server responsive. The
// prior Subscribe registered the subscriber channel *and* closed it, so
// the handler's deferred Unsubscribe double-closed it inside r.mu.Lock —
// the panic skipped the Unlock and wedged every later request on r.mu.
func TestServer_StreamCompletedRun(t *testing.T) {
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))
	dot := `digraph p {
		start [shape=Mdiamond]
		work [prompt="x"]
		done [shape=Msquare]
		start -> work -> done
	}`
	id := submitPipeline(t, srv, dot)
	waitForRunStatus(t, srv, id, "completed")

	// Stream events for the now-completed run (the UI's replay path).
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/events")
	must(t, err)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// The server must still answer promptly; a leaked r.mu would hang this.
	client := &http.Client{Timeout: 3 * time.Second}
	r2, err := client.Get(srv.URL() + "/pipelines/" + id)
	if err != nil {
		t.Fatalf("summary after stream failed (deadlock?): %v", err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("summary status=%d after stream", r2.StatusCode)
	}
}
