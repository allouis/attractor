package server

import (
	"bufio"
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

// Manifest is the on-disk metadata for a single run. Written when the
// run is queued and updated again when it terminates so a server
// restart can reconstruct the registry from disk alone.
type Manifest struct {
	ID            string    `json:"id"`
	Status        RunStatus `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
	GraphName     string    `json:"graph_name,omitempty"`
	GraphGoal     string    `json:"graph_goal,omitempty"`
	Outcome       string    `json:"outcome,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	LogsRoot      string    `json:"logs_root"`
}

// runRegistry holds active and completed runs by ID.
type runRegistry struct {
	mu       sync.RWMutex
	runs     map[string]*Run
	baseDir  string
}

func newRunRegistry(baseDir string) *runRegistry {
	r := &runRegistry{runs: map[string]*Run{}, baseDir: baseDir}
	r.reload()
	return r
}

// reload scans the base directory for prior runs and reconstructs the
// in-memory index. Runs whose manifest reports `running` get marked
// `cancelled` since the server obviously didn't survive the restart.
func (r *runRegistry) reload() {
	if r.baseDir == "" {
		return
	}
	entries, err := os.ReadDir(r.baseDir)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		path := filepath.Join(r.baseDir, ent.Name(), "manifest.json")
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			continue
		}
		status := m.Status
		if status == RunRunning || status == RunQueued {
			status = RunCancelled
		}
		run := &Run{
			ID:          m.ID,
			logsRoot:    m.LogsRoot,
			status:      status,
			startedAt:   m.StartedAt,
			completedAt: m.CompletedAt,
			subscribers: map[chan engine.Event]struct{}{},
			questions:   map[string]*pendingQuestion{},
			persisted:   true,
		}
		if m.Outcome != "" {
			run.outcome = &engine.Outcome{
				Status:        engine.ParseStatus(m.Outcome),
				StatusString:  m.Outcome,
				FailureReason: m.FailureReason,
			}
		}
		r.runs[m.ID] = run
	}
}

func (r *runRegistry) NewRun(source string, g *graph.Graph, prepared *engine.PreparedGraph, baseDir string, makeHandlers HandlerFactory) *Run {
	id := newRunID()
	logsRoot := filepath.Join(baseDir, id)
	run := &Run{
		ID:          id,
		source:      source,
		graph:       g,
		prepared:    prepared,
		logsRoot:    logsRoot,
		status:      RunQueued,
		startedAt:   time.Now(),
		factory:     makeHandlers,
		subscribers: map[chan engine.Event]struct{}{},
		questions:   map[string]*pendingQuestion{},
		persisted:   true,
	}
	run.writeSource()
	run.writeManifest()
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

// List returns a snapshot of all run IDs known to the registry sorted
// by start time descending. Useful for a GET /pipelines index endpoint
// (not yet wired).
func (r *runRegistry) List() []*Run {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Run, 0, len(r.runs))
	for _, run := range r.runs {
		out = append(out, run)
	}
	return out
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
	persisted   bool

	history     []engine.Event
	subscribers map[chan engine.Event]struct{}

	questions map[string]*pendingQuestion
}

type pendingQuestion struct {
	question interviewer.Question
	answer   chan interviewer.Answer
	nodeID   string
}

// Source returns the original DOT source for the run. Tries the
// in-memory copy first, then falls back to the on-disk source.dot
// (useful after a server restart reloads runs from disk).
func (r *Run) Source() string {
	if r.source != "" {
		return r.source
	}
	data, err := os.ReadFile(filepath.Join(r.logsRoot, "source.dot"))
	if err != nil {
		return ""
	}
	return string(data)
}

// Subscribe registers a new SSE consumer and replays the buffered
// history into the returned channel before live events stream in.
func (r *Run) Subscribe() chan engine.Event {
	ch := make(chan engine.Event, 128)
	r.mu.Lock()
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
		// Replay disk history if no in-memory history (resumed run).
		if len(r.history) == 0 {
			for _, ev := range r.replayEvents() {
				select {
				case ch <- ev:
				default:
				}
			}
		}
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

// Cancel marks the run for cancellation.
func (r *Run) Cancel() {
	r.mu.Lock()
	r.cancelled = true
	if r.status == RunQueued || r.status == RunRunning {
		r.status = RunCancelled
	}
	r.mu.Unlock()
	r.writeManifest()
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
	r.writeManifest()

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
	for ch := range r.subscribers {
		close(ch)
	}
	r.subscribers = map[chan engine.Event]struct{}{}
	r.mu.Unlock()
	r.writeManifest()
}

func (r *Run) fanOutEvents(src <-chan engine.Event, done chan<- struct{}) {
	eventsFile := r.openEventsFile()
	defer func() {
		if eventsFile != nil {
			_ = eventsFile.Close()
		}
		close(done)
	}()
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
			}
		}
		if eventsFile != nil {
			if data, err := json.Marshal(ev); err == nil {
				eventsFile.Write(data)
				eventsFile.Write([]byte("\n"))
			}
		}
	}
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

// writeManifest persists the run's manifest.json.
func (r *Run) writeManifest() {
	if !r.persisted || r.logsRoot == "" {
		return
	}
	r.mu.RLock()
	m := Manifest{
		ID:          r.ID,
		Status:      r.status,
		StartedAt:   r.startedAt,
		CompletedAt: r.completedAt,
		LogsRoot:    r.logsRoot,
	}
	if r.graph != nil {
		m.GraphName = r.graph.Name
		m.GraphGoal = r.graph.Goal()
	}
	if r.outcome != nil {
		m.Outcome = r.outcome.Status.String()
		m.FailureReason = r.outcome.FailureReason
	}
	r.mu.RUnlock()
	_ = os.MkdirAll(r.logsRoot, 0o755)
	data, _ := json.MarshalIndent(m, "", "  ")
	_ = os.WriteFile(filepath.Join(r.logsRoot, "manifest.json"), data, 0o644)
}

// writeSource snapshots the DOT source the run was created from. Used
// by GET /pipelines/{id}/graph (SVG render) after server restarts.
func (r *Run) writeSource() {
	if r.source == "" {
		return
	}
	_ = os.MkdirAll(r.logsRoot, 0o755)
	_ = os.WriteFile(filepath.Join(r.logsRoot, "source.dot"), []byte(r.source), 0o644)
}

// openEventsFile opens events.jsonl in append mode for fan-out.
func (r *Run) openEventsFile() *os.File {
	if r.logsRoot == "" {
		return nil
	}
	if err := os.MkdirAll(r.logsRoot, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(r.logsRoot, "events.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return f
}

// replayEvents reads and parses the persisted events.jsonl. Used by SSE
// subscribers that connect after a run completes on disk-only history.
func (r *Run) replayEvents() []engine.Event {
	if r.logsRoot == "" {
		return nil
	}
	f, err := os.Open(filepath.Join(r.logsRoot, "events.jsonl"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []engine.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var ev engine.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
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
