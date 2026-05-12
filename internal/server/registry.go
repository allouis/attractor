package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/graph"
	"github.com/fabro/attractor/internal/interviewer"
)

// RunStatus is the lifecycle state of a pipeline.
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
	RunCancelled RunStatus = "cancelled"
)

// runRegistry holds active and completed runs by ID.
type runRegistry struct {
	mu   sync.RWMutex
	runs map[string]*Run
}

func newRunRegistry() *runRegistry {
	return &runRegistry{runs: map[string]*Run{}}
}

func (r *runRegistry) NewRun(source string, g *graph.Graph, prepared *engine.PreparedGraph, logsRoot string, makeHandlers HandlerFactory) *Run {
	run := &Run{
		ID:        newRunID(),
		source:    source,
		graph:     g,
		prepared:  prepared,
		logsRoot:  filepath.Join(logsRoot, time.Now().Format("20060102-150405")+"-"+newRunID()[:6]),
		status:    RunQueued,
		startedAt: time.Now(),
		factory:   makeHandlers,
		subscribers: map[chan engine.Event]struct{}{},
		questions: map[string]*pendingQuestion{},
	}
	r.mu.Lock()
	r.runs[run.ID] = run
	r.mu.Unlock()
	return run
}

func (r *runRegistry) Get(id string) (*Run, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	run, ok := r.runs[id]
	return run, ok
}

// Run is one in-memory pipeline execution.
type Run struct {
	ID string

	source   string
	graph    *graph.Graph
	prepared *engine.PreparedGraph
	logsRoot string
	factory  HandlerFactory

	mu          sync.RWMutex
	status      RunStatus
	startedAt   time.Time
	completedAt time.Time
	outcome     *engine.Outcome
	failure     string
	cancelled   bool

	history     []engine.Event
	subscribers map[chan engine.Event]struct{}

	questions map[string]*pendingQuestion
}

type pendingQuestion struct {
	question interviewer.Question
	answer   chan interviewer.Answer
	nodeID   string
}

// Source returns the original DOT source for the run.
func (r *Run) Source() string { return r.source }

// Subscribe registers a new SSE consumer and replays the buffered
// history into the returned channel before live events stream in.
func (r *Run) Subscribe() chan engine.Event {
	ch := make(chan engine.Event, 128)
	r.mu.Lock()
	// Replay history immediately.
	for _, ev := range r.history {
		select {
		case ch <- ev:
		default:
		}
	}
	r.subscribers[ch] = struct{}{}
	finished := r.status == RunCompleted || r.status == RunFailed || r.status == RunCancelled
	r.mu.Unlock()
	if finished {
		close(ch)
	}
	return ch
}

// Unsubscribe removes the subscriber and closes its channel.
func (r *Run) Unsubscribe(ch chan engine.Event) {
	r.mu.Lock()
	if _, ok := r.subscribers[ch]; ok {
		delete(r.subscribers, ch)
		close(ch)
	}
	r.mu.Unlock()
}

// Cancel marks the run for cancellation. The engine doesn't yet support
// cooperative cancellation, so for MVP this records the intent and
// surfaces it in Summary; long-running handlers should check
// run.IsCancelled to bail early.
func (r *Run) Cancel() {
	r.mu.Lock()
	r.cancelled = true
	if r.status == RunQueued || r.status == RunRunning {
		r.status = RunCancelled
	}
	r.mu.Unlock()
}

// IsCancelled reports whether Cancel has been called.
func (r *Run) IsCancelled() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cancelled
}

// Summary returns a JSON-friendly snapshot.
func (r *Run) Summary() map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	resp := map[string]any{
		"id":         r.ID,
		"status":     r.status,
		"started_at": r.startedAt.Format(time.RFC3339Nano),
		"logs_root":  r.logsRoot,
		"events":     len(r.history),
	}
	if !r.completedAt.IsZero() {
		resp["completed_at"] = r.completedAt.Format(time.RFC3339Nano)
	}
	if r.outcome != nil {
		resp["outcome"] = r.outcome.Status.String()
		if r.outcome.FailureReason != "" {
			resp["failure_reason"] = r.outcome.FailureReason
		}
	}
	return resp
}

// PendingQuestions returns all unanswered questions.
func (r *Run) PendingQuestions() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []map[string]any
	for id, q := range r.questions {
		out = append(out, questionToMap(id, q))
	}
	return out
}

// SubmitAnswer fulfills the answer channel for the named question.
func (r *Run) SubmitAnswer(qid string, payload AnswerPayload) error {
	r.mu.Lock()
	pq, ok := r.questions[qid]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("question %q not pending", qid)
	}
	answer := interviewer.Answer{Value: interviewer.AnswerText, Text: payload.Text}
	if payload.Key != "" || payload.Label != "" {
		opt := &interviewer.Option{Key: payload.Key, Label: payload.Label}
		answer = interviewer.Answer{Value: interviewer.AnswerChoice, SelectedOption: opt}
	}
	select {
	case pq.answer <- answer:
		r.mu.Lock()
		delete(r.questions, qid)
		r.mu.Unlock()
		return nil
	case <-time.After(time.Second):
		return fmt.Errorf("answer delivery timed out")
	}
}

// Checkpoint returns the run's checkpoint.json bytes if any.
func (r *Run) Checkpoint() ([]byte, bool) {
	data, err := os.ReadFile(filepath.Join(r.logsRoot, "checkpoint.json"))
	if err != nil {
		return nil, false
	}
	return data, true
}

// Context returns the latest checkpoint's context-values map.
func (r *Run) Context() map[string]any {
	data, ok := r.Checkpoint()
	if !ok {
		return map[string]any{}
	}
	return parseContextFromCheckpoint(data)
}

func (r *Run) execute() {
	r.mu.Lock()
	r.status = RunRunning
	r.mu.Unlock()

	iv := &remoteInterviewer{run: r}
	registry := r.factory(iv)
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: r.logsRoot, RunID: r.ID})

	done := make(chan struct{})
	go r.fanOutEvents(eng.Events(), done)
	outcome, err := eng.Run(r.prepared)
	<-done

	r.mu.Lock()
	r.completedAt = time.Now()
	r.outcome = &outcome
	if outcome.Status == engine.StatusSuccess || outcome.Status == engine.StatusPartialSuccess {
		r.status = RunCompleted
	} else {
		r.status = RunFailed
		if err != nil {
			r.failure = err.Error()
		}
	}
	if r.cancelled {
		r.status = RunCancelled
	}
	// Close any active subscribers — pipeline is done.
	for ch := range r.subscribers {
		close(ch)
	}
	r.subscribers = map[chan engine.Event]struct{}{}
	r.mu.Unlock()
}

func (r *Run) fanOutEvents(src <-chan engine.Event, done chan<- struct{}) {
	for ev := range src {
		r.mu.Lock()
		r.history = append(r.history, ev)
		subs := make([]chan engine.Event, 0, len(r.subscribers))
		for ch := range r.subscribers {
			subs = append(subs, ch)
		}
		r.mu.Unlock()
		for _, ch := range subs {
			select {
			case ch <- ev:
			default:
				// subscriber is slow; drop the event for them.
			}
		}
	}
	close(done)
}

// registerQuestion is used by RemoteInterviewer to enqueue a question
// and obtain its answer channel.
func (r *Run) registerQuestion(q interviewer.Question, nodeID string) (string, chan interviewer.Answer) {
	qid := q.ID
	if qid == "" {
		qid = newRunID()[:8]
	}
	ch := make(chan interviewer.Answer, 1)
	r.mu.Lock()
	r.questions[qid] = &pendingQuestion{question: q, answer: ch, nodeID: nodeID}
	r.mu.Unlock()
	return qid, ch
}

func questionToMap(id string, q *pendingQuestion) map[string]any {
	opts := make([]map[string]any, 0, len(q.question.Options))
	for _, o := range q.question.Options {
		opts = append(opts, map[string]any{"key": o.Key, "label": o.Label})
	}
	return map[string]any{
		"id":      id,
		"node_id": q.nodeID,
		"text":    q.question.Text,
		"options": opts,
	}
}

// parseContextFromCheckpoint extracts the `context` map from a raw
// checkpoint.json without depending on the engine.Checkpoint type to
// avoid cyclic imports between server and engine.
func parseContextFromCheckpoint(data []byte) map[string]any {
	var v struct {
		Context map[string]any `json:"context"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return map[string]any{}
	}
	return v.Context
}
