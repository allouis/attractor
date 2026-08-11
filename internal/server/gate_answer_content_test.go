package server

import (
	"testing"
	"time"

	"github.com/allouis/attractor/internal/engine"
)

// A direct run's answered gate persists the human's choice on the
// interview_answered event, so a reloaded/replayed run — which the feed
// rebuilds solely from the event history the daemon serves over SSE — can show
// the chosen option and note. R2 found this was the one lossy point: the
// answered event recorded no answer content, so a gate turn could not name the
// decision after the fact. The fix is at the source (WaitHuman.Execute); this
// asserts it survives into the daemon's server-side event history.
func TestAnsweredGateEventCarriesChoiceServerSide(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp, Launcher: NewDirectLauncher()})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	src := `digraph d {
		start [shape=Mdiamond]
		gate  [shape=hexagon, label="Approve the change?"]
		done  [shape=Msquare]
		start -> gate
		gate  -> done [label="[A] Approve"]
	}`
	id, err := srv.submit(src, nil, tmp, "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	q := waitQuestion(t, srv, id, 30*time.Second)
	gateID, _ := q["id"].(string)
	if gateID == "" {
		t.Fatalf("gate question has no id: %v", q)
	}

	run, ok := srv.registry.Get(id)
	if !ok {
		t.Fatal("run not found")
	}
	if err := run.SubmitAnswer(gateID, AnswerPayload{Key: "A", Label: "[A] Approve", Text: "looks good to me"}); err != nil {
		t.Fatalf("SubmitAnswer: %v", err)
	}
	waitTerminal(t, srv, id, 30*time.Second)

	var answered *engine.Event
	for _, ev := range run.historySnapshot() {
		if ev.Kind == engine.EventInterviewAnswered {
			e := ev
			answered = &e
		}
	}
	if answered == nil {
		t.Fatal("no interview_answered event in the server-side history")
	}
	if answered.Detail["label"] != "[A] Approve" {
		t.Errorf("answered label = %q, want [A] Approve (derivable server-side)", answered.Detail["label"])
	}
	if answered.Detail["note"] != "looks good to me" {
		t.Errorf("answered note = %q, want the human's note", answered.Detail["note"])
	}
}
