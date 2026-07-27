package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/engine"
)

// TestStreamEventsSinceReplaysAfterCursor seeds a finished run's events.jsonl
// with more than 130 events and connects with ?since=N, asserting only events
// with seq > N are delivered and the terminal event still arrives (T3).
func TestStreamEventsSinceReplaysAfterCursor(t *testing.T) {
	srv, tmp := newStageTestServer(t)
	logsRoot := filepath.Join(tmp, "r1")
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	const n = 140
	var buf strings.Builder
	for i := 1; i <= n; i++ {
		line, _ := json.Marshal(engine.Event{Kind: engine.EventStageProgress, Seq: int64(i)})
		buf.Write(line)
		buf.WriteByte('\n')
	}
	term, _ := json.Marshal(engine.Event{Kind: engine.EventPipelineCompleted, Seq: n + 1})
	buf.Write(term)
	buf.WriteByte('\n')
	if err := os.WriteFile(filepath.Join(logsRoot, "events.jsonl"), []byte(buf.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	srv.registry.mu.Lock()
	srv.registry.runs["r1"] = &Run{
		ID:          "r1",
		logsRoot:    logsRoot,
		status:      RunCompleted,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
	}
	srv.registry.mu.Unlock()

	const since = 100
	resp, err := http.Get(srv.URL() + "/pipelines/r1/events?since=" + strconv.Itoa(since))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	var got []engine.Event
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		payload, ok := strings.CutPrefix(sc.Text(), "data:")
		if !ok {
			continue
		}
		var ev engine.Event
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &ev); err != nil {
			t.Fatalf("bad SSE frame: %v", err)
		}
		got = append(got, ev)
	}

	if len(got) != n+1-since {
		t.Fatalf("delivered %d events, want %d", len(got), n+1-since)
	}
	for _, ev := range got {
		if ev.Seq <= since {
			t.Errorf("event seq %d <= since %d leaked through", ev.Seq, since)
		}
	}
	if last := got[len(got)-1]; last.Kind != engine.EventPipelineCompleted {
		t.Errorf("terminal event dropped; last = %q", last.Kind)
	}
}
