package runserver

import (
	"fmt"
	"sync"
	"time"

	"github.com/allouis/attractor/internal/interviewer"
)

// Gate is the HTTP-answerable interviewer for `attractor run --ui`
// (local-first plan: human gates are answered on the run's own
// /questions + /answer surface). Ask blocks the pipeline until Answer —
// called from the server's POST handler — resolves the question, or the
// question's own timeout fires (so human.default_choice still applies
// to unattended runs).
type Gate struct {
	mu      sync.Mutex
	pending map[string]pendingGate
}

type pendingGate struct {
	q  interviewer.Question
	ch chan interviewer.Answer
}

// NewGate returns an empty Gate.
func NewGate() *Gate {
	return &Gate{pending: map[string]pendingGate{}}
}

// Ask implements interviewer.Interviewer.
func (g *Gate) Ask(q interviewer.Question) (interviewer.Answer, error) {
	ch := make(chan interviewer.Answer, 1)
	g.mu.Lock()
	g.pending[q.ID] = pendingGate{q: q, ch: ch}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		delete(g.pending, q.ID)
		g.mu.Unlock()
	}()
	if q.Timeout > 0 {
		select {
		case a := <-ch:
			return a, nil
		case <-time.After(q.Timeout):
			return interviewer.Answer{Value: interviewer.AnswerTimeout}, nil
		}
	}
	return <-ch, nil
}

// Answer resolves a pending question. value matches an option by key
// or label (multiple choice) or is free text otherwise; note carries
// the optional human note ($context.human.note).
func (g *Gate) Answer(questionID, value, note string) error {
	g.mu.Lock()
	p, ok := g.pending[questionID]
	if ok {
		delete(g.pending, questionID)
	}
	g.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending question %q", questionID)
	}
	a := interviewer.Answer{Value: interviewer.AnswerText, Text: note}
	for _, opt := range p.q.Options {
		if opt.Key == value || opt.Label == value {
			o := opt
			a.Value = interviewer.AnswerChoice
			a.SelectedOption = &o
			break
		}
	}
	if a.SelectedOption == nil && value != "" && len(p.q.Options) > 0 {
		// Unrecognized option on a multiple-choice question: refuse
		// rather than guess — requeue so the client can retry.
		g.mu.Lock()
		g.pending[questionID] = p
		g.mu.Unlock()
		return fmt.Errorf("no option %q on question %q", value, questionID)
	}
	if a.Text == "" && a.SelectedOption == nil {
		a.Text = value
	}
	p.ch <- a
	return nil
}
