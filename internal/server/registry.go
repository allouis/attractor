package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/interviewer"
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
	ID            string        `json:"id"`
	Status        RunStatus     `json:"status"`
	StartedAt     time.Time     `json:"started_at"`
	CompletedAt   time.Time     `json:"completed_at,omitempty"`
	GraphName     string        `json:"graph_name,omitempty"`
	WorkflowName  string        `json:"workflow_name,omitempty"`
	GraphGoal     string        `json:"graph_goal,omitempty"`
	Cwd           string        `json:"cwd,omitempty"`
	Outcome       string        `json:"outcome,omitempty"`
	FailureReason string        `json:"failure_reason,omitempty"`
	LogsRoot      string        `json:"logs_root"`
	Tokens        *engine.Usage `json:"tokens,omitempty"`
	ItemRef       string        `json:"item_ref,omitempty"`
}

// runRegistry holds active and completed runs by ID.
type runRegistry struct {
	mu      sync.RWMutex
	runs    map[string]*Run
	baseDir string
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
		// Skip runs that carry no id: a daemon run always has one stamped at
		// creation, so an id-less manifest is a standalone `attractor run`
		// dir sharing the logs root. Loading them would collapse every such
		// run into a single broken r.runs[""] entry in the fleet view.
		if m.ID == "" {
			continue
		}
		status := m.Status
		if status == RunRunning || status == RunQueued {
			status = RunCancelled
		}
		run := &Run{
			ID:           m.ID,
			logsRoot:     m.LogsRoot,
			status:       status,
			startedAt:    m.StartedAt,
			completedAt:  m.CompletedAt,
			graphName:    m.GraphName,
			workflowName: m.WorkflowName,
			cwd:          m.Cwd,
			itemRef:      m.ItemRef,
			subscribers:  map[chan engine.Event]struct{}{},
			questions:    map[string]*pendingQuestion{},
			persisted:    true,
		}
		if m.Outcome != "" {
			run.outcome = &engine.Outcome{
				Status:        engine.ParseStatus(m.Outcome),
				StatusString:  m.Outcome,
				FailureReason: m.FailureReason,
			}
		}
		if m.Tokens != nil {
			run.usage = *m.Tokens
		}
		r.runs[m.ID] = run
	}
}

// NewRun mints a run. itemRef, when non-empty, is the opaque tag naming
// the external Item that spawned it (items-spec I1). workflowName, when
// non-empty, is the catalog directory the run was dispatched from — the
// handle the run→workflow backlink resolves against GET /workflows/{name}
// (web-ui-spec W6); a raw dot submission (POST /pipelines) has none. Both
// are stamped at creation, persisted in the manifest, and surfaced in the
// run summary.
func (r *runRegistry) NewRun(source string, g *graph.Graph, prepared *engine.PreparedGraph, baseDir string, makeHandlers HandlerFactory, itemRef, workflowName string, initialContext map[string]string) *Run {
	id := newRunID()
	logsRoot := filepath.Join(baseDir, id)
	run := &Run{
		ID:             id,
		token:          newRunID(),
		source:         source,
		graph:          g,
		prepared:       prepared,
		logsRoot:       logsRoot,
		status:         RunQueued,
		startedAt:      time.Now(),
		factory:        makeHandlers,
		itemRef:        itemRef,
		workflowName:   workflowName,
		initialContext: initialContext,
		subscribers:    map[chan engine.Event]struct{}{},
		questions:      map[string]*pendingQuestion{},
		persisted:      true,
	}
	if g != nil {
		run.graphName = g.Name
		run.cwd = g.Attrs["cwd"]
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

// List returns a snapshot of all runs known to the registry sorted by
// start time descending (newest first), backing GET /pipelines.
func (r *runRegistry) List() []*Run {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Run, 0, len(r.runs))
	for _, run := range r.runs {
		out = append(out, run)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].startedAt.After(out[j].startedAt)
	})
	return out
}

// RunsForItem returns the runs stamped with the item tag, newest first.
// Grouping is plain string equality. It backs GET /items' linked-run
// annotation (items-spec §11): the registry is the sole source of truth
// for which runs an Item has spawned.
func (r *runRegistry) RunsForItem(ref string) []*Run {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Run
	for _, run := range r.runs {
		if ref != "" && run.itemRef == ref {
			out = append(out, run)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].startedAt.After(out[j].startedAt)
	})
	return out
}

// Run is one in-memory pipeline execution.
type Run struct {
	ID string

	source       string
	graph        *graph.Graph
	prepared     *engine.PreparedGraph
	logsRoot     string
	factory      HandlerFactory
	graphName    string
	workflowName string
	cwd          string
	itemRef      string
	// token authenticates phone-home reporting for this run: a launched
	// child presents it on POST /events, GET /control, POST /artifacts so
	// only the process the daemon started can drive the run.
	token string
	// initialContext seeds the run's context at start (Item vars + item.*
	// metadata); nil for runs with no seed (router-spec deviation B).
	initialContext map[string]string

	mu          sync.RWMutex
	status      RunStatus
	startedAt   time.Time
	completedAt time.Time
	outcome     *engine.Outcome
	failure     string
	cancelled   bool
	persisted   bool
	usage       engine.Usage

	history     []engine.Event
	subscribers map[chan engine.Event]struct{}

	questions map[string]*pendingQuestion
	// answers holds resolved human-gate answers awaiting collection by a
	// polling phone-home child (keyed by question id). In-process runs
	// deliver answers over pendingQuestion.answer instead.
	answers map[string]controlAnswer
}

// polledAnswers returns a copy of the answers awaiting a polling child.
func (r *Run) polledAnswers() map[string]controlAnswer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.answers) == 0 {
		return nil
	}
	out := make(map[string]controlAnswer, len(r.answers))
	for k, v := range r.answers {
		out[k] = v
	}
	return out
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
// history into the returned channel before live events stream in. When
// since > 0 only events with seq > since are replayed, so a client
// resuming a dropped stream (GET ...?since=<seq>) never sees a duplicate
// (tui-spec T3). since <= 0 replays the full history.
func (r *Run) Subscribe(since int64) chan engine.Event {
	r.mu.Lock()
	finished := r.status == RunCompleted || r.status == RunFailed || r.status == RunCancelled
	if finished {
		// Snapshot history under the lock: fanOutEvents may still be
		// appending on a cancelled-but-running run, so reading r.history
		// after unlocking would be a data race. No live events will follow
		// a finished run, so deliver the full history in one shot with a
		// buffer sized to fit it — a non-blocking send into a fixed 128-slot
		// buffer would drop the tail, including the terminal event the UI
		// needs to stop reconnecting.
		history := append([]engine.Event(nil), r.history...)
		r.mu.Unlock()
		if len(history) == 0 {
			// Resumed run: history lives only on disk.
			history = r.replayEvents()
		}
		ch := make(chan engine.Event, len(history)+1)
		for _, ev := range history {
			if since > 0 && ev.Seq <= since {
				continue
			}
			ch <- ev
		}
		close(ch)
		return ch
	}
	// Live run: replay buffered history, then register for live events.
	ch := make(chan engine.Event, 128)
	for _, ev := range r.history {
		if since > 0 && ev.Seq <= since {
			continue
		}
		select {
		case ch <- ev:
		default:
		}
	}
	r.subscribers[ch] = struct{}{}
	r.mu.Unlock()
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
	queued := r.status == RunQueued
	if r.status == RunQueued || r.status == RunRunning {
		r.status = RunCancelled
	}
	// A queued run never reaches execute(), so nothing else would ever
	// close its subscribers. Terminate them here, in the same critical
	// section as the status transition, mirroring execute()'s terminal
	// close (no double-close window). A running run is left to execute().
	if queued {
		for ch := range r.subscribers {
			close(ch)
		}
		r.subscribers = map[chan engine.Event]struct{}{}
	}
	r.mu.Unlock()
	r.writeManifest()
}

// isTerminal reports whether the run has reached a final state.
func (r *Run) isTerminal() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status == RunCompleted || r.status == RunFailed || r.status == RunCancelled
}

// failCrashed marks a non-terminal run failed because the process driving
// it (a launched subprocess/VM) exited without reporting a terminal event.
// Idempotent: a run already terminal is left untouched.
func (r *Run) failCrashed(reason string) {
	r.mu.Lock()
	if r.status == RunCompleted || r.status == RunFailed || r.status == RunCancelled {
		r.mu.Unlock()
		return
	}
	r.completedAt = time.Now()
	r.status = RunFailed
	r.failure = reason
	r.outcome = &engine.Outcome{Status: engine.StatusFail, StatusString: engine.StatusFail.String(), FailureReason: reason}
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

// Status returns the run's current lifecycle state under the lock.
func (r *Run) Status() RunStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
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
		"graph_name": r.graphName,
		"cwd":        r.cwd,
		// needs_human is orthogonal to status: a running run blocked on a
		// wait.human question still reports "running". The fleet view counts
		// and flags this as the highest-attention state (web-ui-spec W5).
		"needs_human": len(r.questions) > 0,
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
	if r.usage.InputTokens != 0 || r.usage.OutputTokens != 0 {
		resp["tokens"] = r.usage
	}
	if r.itemRef != "" {
		resp["item_ref"] = r.itemRef
	}
	if r.workflowName != "" {
		resp["workflow_name"] = r.workflowName
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
	eng := engine.New(engine.Config{Registry: registry, LogsRoot: r.logsRoot, RunID: r.ID, InitialContext: r.initialContext})

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

// fanOutEvents buffers events in memory and fans them out to live SSE
// subscribers. Durable persistence to events.jsonl is the engine's job
// (it writes the same LogsRoot), so this loop no longer touches disk.
func (r *Run) fanOutEvents(src <-chan engine.Event, done chan<- struct{}) {
	defer close(done)
	for ev := range src {
		r.deliver(ev)
	}
}

// deliver records one event in the run's history + usage rollup and fans
// it out to live SSE subscribers. Shared by the in-process engine loop
// (fanOutEvents) and phone-home ingest (Ingest).
func (r *Run) deliver(ev engine.Event) {
	r.mu.Lock()
	r.history = append(r.history, ev)
	if ev.Kind == engine.EventUsage && ev.Usage != nil {
		r.usage.Add(*ev.Usage)
	}
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
}

// Token returns the run's phone-home auth token.
func (r *Run) Token() string { return r.token }

// Ingest records an event reported by a phone-home child: it fans the
// event out to subscribers and appends it to the daemon's own
// events.jsonl (the child persists to its own FS, unreachable here).
func (r *Run) Ingest(ev engine.Event) {
	r.deliver(ev)
	r.appendEvent(ev)
	switch ev.Kind {
	case engine.EventPipelineStarted:
		r.markRunning()
	case engine.EventPipelineCompleted, engine.EventPipelineFailed:
		r.finishFromEvent(ev)
	}
}

// markRunning transitions a queued phone-home run to running when its child
// reports pipeline_started (the daemon does not drive it in-process, so
// nothing else flips the status). A run past queued is left untouched.
func (r *Run) markRunning() {
	r.mu.Lock()
	if r.status == RunQueued {
		r.status = RunRunning
	}
	r.mu.Unlock()
	r.writeManifest()
}

// finishFromEvent transitions a phone-home run to its terminal state from
// the ingested terminal pipeline event. It mirrors execute()'s terminal
// block (status, outcome, completedAt, subscriber close) for runs the
// daemon does not drive in-process. Idempotent: a run already terminal
// (e.g. cancelled) is left untouched.
func (r *Run) finishFromEvent(ev engine.Event) {
	r.mu.Lock()
	if r.status == RunCompleted || r.status == RunFailed || r.status == RunCancelled {
		r.mu.Unlock()
		return
	}
	r.completedAt = time.Now()
	if ev.Kind == engine.EventPipelineCompleted {
		r.status = RunCompleted
		r.outcome = &engine.Outcome{Status: engine.ParseStatus(ev.Status), StatusString: ev.Status}
	} else {
		r.status = RunFailed
		r.outcome = &engine.Outcome{Status: engine.StatusFail, StatusString: engine.StatusFail.String(), FailureReason: ev.Message}
		r.failure = ev.Message
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

// appendEvent appends one event as a JSON line to the run's events.jsonl.
func (r *Run) appendEvent(ev engine.Event) {
	if r.logsRoot == "" {
		return
	}
	_ = os.MkdirAll(r.logsRoot, 0o755)
	f, err := os.OpenFile(filepath.Join(r.logsRoot, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	line, err := json.Marshal(ev)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
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
	m.GraphName = r.graphName
	m.WorkflowName = r.workflowName
	m.Cwd = r.cwd
	m.ItemRef = r.itemRef
	if r.graph != nil {
		m.GraphGoal = r.graph.Goal()
	}
	if r.outcome != nil {
		m.Outcome = r.outcome.Status.String()
		m.FailureReason = r.outcome.FailureReason
	}
	if r.usage.InputTokens != 0 || r.usage.OutputTokens != 0 {
		u := r.usage
		m.Tokens = &u
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
