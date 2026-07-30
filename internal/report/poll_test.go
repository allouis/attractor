package report

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/interviewer"
)

// PollInterviewer.Ask blocks until the daemon reports an answer, then maps
// it to the selected option — the phone-home half of a human gate.
func TestPollInterviewerReturnsAnswerWhenAvailable(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pipelines/{id}/control", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			fmt.Fprint(w, `{"cancel":false}`) // not answered yet
			return
		}
		fmt.Fprint(w, `{"cancel":false,"answers":{"gate-1":{"key":"A","label":"Approve"}}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	iv := NewPollInterviewer(New(srv.URL, "run1", "tok"), time.Millisecond)
	ans, err := iv.Ask(interviewer.Question{ID: "gate-1"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Value != interviewer.AnswerChoice || ans.SelectedOption == nil || ans.SelectedOption.Label != "Approve" {
		t.Fatalf("answer = %+v, want Approve choice", ans)
	}
}

// A question with a Timeout returns AnswerTimeout when no answer arrives in
// time, so the handler can apply human.default_choice (spec §6.5).
func TestPollInterviewerHonorsTimeout(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pipelines/{id}/control", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"cancel":false}`) // never answered
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	iv := NewPollInterviewer(New(srv.URL, "run1", "tok"), 5*time.Millisecond)
	ans, err := iv.Ask(interviewer.Question{ID: "gate-1", Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Value != interviewer.AnswerTimeout {
		t.Fatalf("answer = %+v, want timeout", ans)
	}
}

// A cancel resolves the gate as SKIPPED so the run unwinds.
func TestPollInterviewerCancelSkips(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pipelines/{id}/control", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"cancel":true}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	iv := NewPollInterviewer(New(srv.URL, "run1", "tok"), time.Millisecond)
	ans, err := iv.Ask(interviewer.Question{ID: "gate-1"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Value != interviewer.AnswerSkipped {
		t.Fatalf("answer = %+v, want skipped on cancel", ans)
	}
}
