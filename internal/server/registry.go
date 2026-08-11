package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	Repo          string        `json:"repo,omitempty"`
	// InitialContext is the run's seeded context (Item vars + item.* metadata)
	// stamped at dispatch. Persisted so a reloaded run still surfaces the item's
	// human identifier/title/url in /state — a naive reload drops it and /state
	// falls back to the item_ref external id (the tracker UUID).
	InitialContext map[string]string `json:"initial_context,omitempty"`
	// SourceBaseDir is the directory the pipeline was loaded from — the root its
	// @file prompt refs resolve against. Persisted so a reloaded run (whose graph
	// is not in memory) re-prepares its source against the right base and serves
	// resolved prompt text, not the raw @file ref.
	SourceBaseDir string `json:"source_base_dir,omitempty"`
	// RevBase/RevTip are the host jj change-ids of the run's cwd at start and
	// end (T9c): the range `jj diff --from RevBase --to RevTip` renders as the
	// run's produced change. Empty for VM/non-jj runs.
	RevBase string `json:"rev_base,omitempty"`
	RevTip  string `json:"rev_tip,omitempty"`
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
			ID:             m.ID,
			logsRoot:       m.LogsRoot,
			status:         status,
			startedAt:      m.StartedAt,
			completedAt:    m.CompletedAt,
			graphName:      m.GraphName,
			workflowName:   m.WorkflowName,
			cwd:            m.Cwd,
			sourceBaseDir:  m.SourceBaseDir,
			itemRef:        m.ItemRef,
			repo:           m.Repo,
			initialContext: m.InitialContext,
			revBase:        m.RevBase,
			revTip:         m.RevTip,
			subscribers:    map[chan engine.Event]struct{}{},
			questions:      map[string]*pendingQuestion{},
			persisted:      true,
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
// (web-ui-spec W6); a raw dot submission (POST /pipelines) has none. repo,
// when non-empty, is the registered `owner/name` the run was dispatched
// against (fleet repo provenance, web-ui-v2-spec U7). All are stamped at
// creation, persisted in the manifest, and surfaced in the run summary.
func (r *runRegistry) NewRun(source string, g *graph.Graph, prepared *engine.PreparedGraph, baseDir string, makeHandlers HandlerFactory, itemRef, workflowName, repo string, initialContext map[string]string) *Run {
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
		repo:           repo,
		initialContext: initialContext,
		subscribers:    map[chan engine.Event]struct{}{},
		questions:      map[string]*pendingQuestion{},
		persisted:      true,
	}
	if g != nil {
		run.graphName = g.Name
		run.cwd = g.Attrs["cwd"]
		run.sourceBaseDir = g.BaseDir
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
	// sourceBaseDir is the directory the pipeline was loaded from — the root its
	// @file prompt refs resolve against (graph.BaseDir for a live run). Held
	// separately so a reloaded run, whose graph is not in memory, can still
	// re-prepare its source against the right base (see baseDir).
	sourceBaseDir string
	itemRef       string
	// repo is the registered `owner/name` this run was dispatched against
	// (the fleet's repo provenance, web-ui-v2-spec U7); "" for a raw-dot
	// submission that named no repo.
	repo string
	// token authenticates phone-home reporting for this run: a launched
	// child presents it on POST /events, GET /control, POST /artifacts so
	// only the process the daemon started can drive the run.
	token string
	// placement names the launcher this run requested (V18); "" uses the
	// daemon's default launcher.
	placement string
	// image names the VM boot image this run requested (per-repo VM config,
	// VM1); "" uses the vm launcher's default image. Only meaningful when the
	// resolved placement is "vm". Populated by dispatch resolution in VM3.
	image string
	// revWarned guards the one-shot build-skew warning per run.
	revWarned bool
	// revBase/revTip are the host jj change-ids of cwd at run start and end
	// (T9c); the daemon serves `jj diff --from revBase --to revTip` as the
	// run's produced change. Empty for VM/non-jj runs. Guarded by mu.
	revBase string
	revTip  string
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

// baseDir returns the directory the run's pipeline was loaded from — where
// its @file prompt dependencies resolve. A live run reads it off the in-memory
// graph; a reloaded run (no graph) reads the value persisted in the manifest,
// so re-preparing its stored source still resolves @file refs.
func (r *Run) baseDir() string {
	if r.graph != nil {
		return r.graph.BaseDir
	}
	return r.sourceBaseDir
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
		history = r.reconstructHistory(history)
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
		r.clearQuestions()
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

// clearQuestions drops all pending wait.human questions. Callers hold r.mu.
// A terminal run answers nothing, so clearing at the transition keeps every
// /questions consumer in agreement — the fleet's needs_human flag, the
// run-detail dock and its loud banner all go quiet the moment the run finishes
// (web-ui-v2-spec U5), and a stale SubmitAnswer is refused as not-pending.
func (r *Run) clearQuestions() {
	if len(r.questions) > 0 {
		r.questions = map[string]*pendingQuestion{}
	}
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
	r.clearQuestions()
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

// Cwd returns the run's working directory (the target repo), read under the
// lock so concurrent HTTP handlers race-cleanly.
func (r *Run) Cwd() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cwd
}

// Placement returns the launcher this run requested ("vm", "local", "direct"),
// read under the lock. "" means the daemon's default launcher.
func (r *Run) Placement() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.placement
}

// RevBase / RevTip return the host jj commit_ids stamped at run start / end
// (T9c). Empty when no host range was recorded (VM/non-jj run).
func (r *Run) RevBase() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.revBase
}

func (r *Run) RevTip() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.revTip
}

// resolveTip returns the run's diff tip commit and whether it is resolvable. A
// vm run's produced change lives in its per-run jj workspace in the shared host
// store, so the tip is that workspace's working-copy commit — resolved LIVE (a
// pure read) so the diff reflects work so far, and naturally "not computable"
// once the reaper forgets the workspace. direct and local runs stamp their tip
// at terminal into revTip, so their behavior is unchanged.
func (r *Run) resolveTip() (string, bool) {
	if r.Placement() == "vm" {
		id, err := jjWorkspaceHead(r.Cwd(), runWorkspaceName(r.ID))
		if err != nil {
			return "", false
		}
		return id, true
	}
	tip := r.RevTip()
	return tip, tip != ""
}

// recordRevBase stamps the run's cwd `@` commit as the diff base, called just
// before launch. It fires for EVERY runner with a jj cwd — direct, local, and
// vm — because a vm run's per-run workspace is based on this same `@` (spec W2),
// so the base is the shared anchor its guest commits diff against (P2).
func (r *Run) recordRevBase() { r.stampRev(func(id string) { r.revBase = id }) }

// recordRevTip stamps the post-run `@` as the tip, called at terminal. It skips
// vm runs: a vm run's commits land in its per-run workspace in the shared store,
// not run.cwd's default working copy, so the host `@` here is not the run's tip.
// getRunDiff resolves a vm run's tip live from that workspace instead (P2).
func (r *Run) recordRevTip() {
	if r.placement == "vm" {
		return
	}
	r.stampRev(func(id string) { r.revTip = id })
}

// stampRev probes the current `@` commit and hands it to set under the lock,
// then persists. It is the shared body of recordRevBase/recordRevTip. A non-jj
// cwd (jjHeadCommit errors) leaves the range empty — the Diff panel then falls
// back to a `*.diff` artifact.
func (r *Run) stampRev(set func(id string)) {
	if r.cwd == "" {
		return
	}
	id, err := jjHeadCommit(r.cwd)
	if err != nil {
		return
	}
	r.mu.Lock()
	set(id)
	r.mu.Unlock()
	r.writeManifest()
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
	if r.repo != "" {
		resp["repo"] = r.repo
	}
	if vars := launchVars(r.initialContext); len(vars) > 0 {
		resp["vars"] = vars
	}
	// resumable gates the re-run-from-failure control: true only when a failed
	// run could actually resume from its checkpoint (web-ui-v2-spec U6). The
	// UI shows re-run-from-failure only when it holds; plain re-run is always
	// available on a terminal run with a known workflow.
	resp["resumable"] = r.resumable()
	return resp
}

// launchVars filters a run's seeded context down to the declared vars a manual
// re-run resubmits (web-ui-v2-spec U6): the item.* metadata and static check.*
// commands are seeded server-side at dispatch, not entered by the human, so
// they are dropped and only the launch inputs (incl. repo) remain.
func launchVars(seed map[string]string) map[string]string {
	if len(seed) == 0 {
		return nil
	}
	out := map[string]string{}
	for k, v := range seed {
		if strings.HasPrefix(k, "item.") || strings.HasPrefix(k, "check.") {
			continue
		}
		out[k] = v
	}
	return out
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
	// Phone-home question (no answer channel): store the answer for the
	// polling child to collect via GET /control (spec §6 — the frontend
	// submits, the headless engine's interviewer fetches).
	if pq.answer == nil {
		r.mu.Lock()
		if r.answers == nil {
			r.answers = map[string]controlAnswer{}
		}
		r.answers[qid] = controlAnswer{Key: payload.Key, Label: payload.Label, Text: payload.Text}
		delete(r.questions, qid)
		r.mu.Unlock()
		r.writeManifest()
		return nil
	}
	answer := interviewer.Answer{Value: interviewer.AnswerText, Text: payload.Text}
	if payload.Key != "" || payload.Label != "" {
		opt := &interviewer.Option{Key: payload.Key, Label: payload.Label}
		// Carry the optional note (Text) alongside the choice so the gate
		// handler can record it as $context.human.note. Dropping it here would
		// lose feedback attached to an option answer (e.g. "[R] Revise" + why).
		answer = interviewer.Answer{Value: interviewer.AnswerChoice, SelectedOption: opt, Text: payload.Text}
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

// resumable reports whether a failed run can be re-run from its checkpoint.
// Two conditions must hold: the run's engine deps are still in memory (a run
// reloaded from disk after a daemon restart is a view-only shell — prepared /
// factory are nil, so relaunching it would nil-func panic in execute), and it
// ran on the direct launcher (checkpoint.json lives under logsRoot on the
// daemon FS; a local/vm run checkpoints inside the now-gone child, so a resume
// would silently fresh-start and re-run every completed, side-effecting node).
// The empty placement is the direct default (server New). web-ui-v2-spec U6.
func (r *Run) resumable() bool {
	if r.prepared == nil || r.factory == nil {
		return false
	}
	return r.placement == "" || r.placement == "direct"
}

// prepareRestart resets a resumable failed run's terminal state so the
// dispatcher can re-launch it (web-ui-v2-spec U6, re-run-from-failure). The
// engine resumes from the on-disk checkpoint at logsRoot — the already-
// completed nodes are skipped and the failed node re-executes — so only the
// terminal bookkeeping is cleared here; the seeded context and checkpoint stay
// put. Reports false (a no-op) when the run is not a resumable failure, so the
// handler can 409 rather than enqueue a run that would crash or silently
// re-run everything.
func (r *Run) prepareRestart() bool {
	r.mu.Lock()
	if r.status != RunFailed || !r.resumable() {
		r.mu.Unlock()
		return false
	}
	r.status = RunQueued
	r.outcome = nil
	r.failure = ""
	r.cancelled = false
	r.completedAt = time.Time{}
	// Drop the failed run's in-memory event history so the reopened run view
	// replays only the resumed timeline.
	r.history = nil
	r.mu.Unlock()
	// Rotate the failed attempt's events.jsonl aside (keeping checkpoint.json,
	// which the resume reads). The resumed run builds a fresh engine whose seq
	// counter restarts at 1 and appends to events.jsonl; leaving the old log in
	// place would duplicate seqs and replay a stale pipeline_failed to any
	// reader whose in-memory history is empty (e.g. after a daemon reboot).
	r.rotateEventLog()
	r.writeManifest()
	return true
}

// rotateEventLog moves the current events.jsonl into the next free _restart_N
// subdirectory, preserving the failed attempt's log for inspection while
// leaving the resumed run a clean file to write. Best-effort: a missing log or
// a rename failure is ignored (the run proceeds without the old log). The
// _restart_ prefix matches the loop-restart archive so nothing replays it.
func (r *Run) rotateEventLog() {
	src := filepath.Join(r.logsRoot, "events.jsonl")
	if _, err := os.Stat(src); err != nil {
		return
	}
	for n := 1; n < 1<<16; n++ {
		dir := filepath.Join(r.logsRoot, fmt.Sprintf("_restart_%d", n))
		if _, err := os.Stat(dir); err == nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		_ = os.Rename(src, filepath.Join(dir, "events.jsonl"))
		return
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
	r.clearQuestions()
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
	case engine.EventInterviewStarted:
		r.registerPhoneHomeQuestion(ev)
	case engine.EventInterviewAnswered, engine.EventInterviewTimeout:
		r.clearQuestion(ev.QuestionID)
	case engine.EventPipelineCompleted, engine.EventPipelineFailed:
		// Only the parent run's own terminal finishes the run. A forwarded
		// child terminal (source=child) is a nested run's verdict; finishing on
		// it would flip the run failed and lock out the real parent terminal
		// (finishFromEvent is idempotent) — T9a on the phone-home path.
		if isOwnTerminal(ev) {
			r.finishFromEvent(ev)
		}
	}
}

// registerPhoneHomeQuestion registers a pending human-gate question from an
// ingested InterviewStarted event (spec §9.6), so the run reports
// needs_human and the existing /questions + /answer surface presents and
// resolves it. The pending question has a nil answer channel, marking it
// phone-home: SubmitAnswer stores the answer for the polling child to
// collect via /control rather than delivering over a channel.
func (r *Run) registerPhoneHomeQuestion(ev engine.Event) {
	if ev.QuestionID == "" || ev.Question == nil {
		return
	}
	q := interviewer.Question{
		ID:    ev.QuestionID,
		Text:  ev.Question.Text,
		Type:  parseQuestionType(ev.Question.Type),
		Stage: ev.Question.Stage,
	}
	for _, o := range ev.Question.Options {
		q.Options = append(q.Options, interviewer.Option{Key: o.Key, Label: o.Label})
	}
	r.mu.Lock()
	if _, exists := r.questions[ev.QuestionID]; !exists {
		r.questions[ev.QuestionID] = &pendingQuestion{question: q, answer: nil, nodeID: ev.NodeID}
	}
	r.mu.Unlock()
	r.writeManifest()
}

// clearQuestion drops a pending question once the child reports it answered
// (InterviewAnswered), covering the case where the child's own interviewer
// resolved it (e.g. a timeout default) without a daemon /answer call.
func (r *Run) clearQuestion(qid string) {
	if qid == "" {
		return
	}
	r.mu.Lock()
	delete(r.questions, qid)
	r.mu.Unlock()
}

// parseQuestionType maps the event's question-type string to the enum.
func parseQuestionType(s string) interviewer.QuestionType {
	switch s {
	case "yes_no":
		return interviewer.QuestionYesNo
	case "confirmation":
		return interviewer.QuestionConfirmation
	case "freeform":
		return interviewer.QuestionFreeform
	default:
		return interviewer.QuestionMultipleChoice
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
	r.clearQuestions()
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
	m.Repo = r.repo
	m.InitialContext = r.initialContext
	m.SourceBaseDir = r.sourceBaseDir
	m.RevBase = r.revBase
	m.RevTip = r.revTip
	if r.graph != nil {
		m.GraphGoal = r.graph.Goal()
		if m.SourceBaseDir == "" {
			m.SourceBaseDir = r.graph.BaseDir
		}
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

// isChildEvent reports whether a pipeline event was forwarded from a nested
// child run. A manager_loop re-emits every child engine event onto the parent
// stream tagged detail.source="child" (handler/manager_loop.go
// consumeChildEvents) — including the child's own terminal pipeline_completed /
// pipeline_failed. Only the PARENT run's terminal ends the run, so the SSE and
// reconstruction paths must not mistake a forwarded child terminal for it (the
// Go mirror of index.html's isChildEvent).
func isChildEvent(ev engine.Event) bool {
	return ev.Detail["source"] == "child"
}

// isOwnTerminal reports whether ev is THIS run's own terminal — a pipeline
// terminal that isn't a forwarded child's. Only an own-terminal ends the run:
// it flips status (finishFromEvent), closes the SSE stream (streamEvents) and
// stops reconstruction repair (resolveInterrupted). A forwarded child terminal
// is just a nested run's verdict and must pass through all three (T9a). The one
// predicate keeps the "does this end the run?" decision from drifting per site.
func isOwnTerminal(ev engine.Event) bool {
	switch ev.Kind {
	case engine.EventPipelineCompleted, engine.EventPipelineFailed:
		return !isChildEvent(ev)
	default:
		return false
	}
}

// reconstructHistory returns the event history a finished-run replay should
// deliver. It prefers the in-memory buffer; when that is empty — a run
// reloaded from disk after a daemon restart, whose in-memory history is gone —
// it replays events.jsonl instead so the graph/timeline still repaint.
func (r *Run) reconstructHistory(inMemory []engine.Event) []engine.Event {
	if len(inMemory) > 0 {
		return inMemory
	}
	return r.resolveInterrupted(r.replayEvents())
}

// resolveInterrupted repairs a disk-replayed history that a daemon restart cut
// off. A run killed mid-flight leaves events.jsonl ending at a stage_started
// with no terminal event, so a naive replay paints that node "executing"
// forever. When the reloaded run is terminal but its log carries no terminal
// pipeline event, synthesize one stage_failed per still-running node (in start
// order, so replay stays deterministic) and a closing pipeline_failed, so the
// graph resolves every node and the stream signals done. A log that already
// ends terminal — a cleanly-finished run — is returned untouched.
func (r *Run) resolveInterrupted(history []engine.Event) []engine.Event {
	if len(history) == 0 || !r.isTerminal() {
		return history
	}
	var (
		maxSeq  int64
		order   []string
		running = map[string]bool{}
	)
	for _, ev := range history {
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
		// Forwarded child events belong to a nested run, not this graph. Skip
		// them for node-state tracking so a child stage never gets a synthetic
		// terminal injected untagged into the parent stream (BLOCKING-2). maxSeq
		// still counts them above so synthetic seqs stay past the whole log.
		if isChildEvent(ev) {
			continue
		}
		switch ev.Kind {
		case engine.EventPipelineCompleted, engine.EventPipelineFailed:
			return history // the run's own terminal is on the log; nothing to repair
		case engine.EventStageStarted, engine.EventStageRetrying:
			if !running[ev.NodeID] {
				order = append(order, ev.NodeID)
			}
			running[ev.NodeID] = true
		case engine.EventStageCompleted, engine.EventStageFailed:
			running[ev.NodeID] = false
		}
	}
	const reason = "interrupted (daemon restart)"
	for _, node := range order {
		if !running[node] {
			continue // already resolved, or a re-entered node emitted once already
		}
		running[node] = false // mark resolved so a twice-entered node fires once
		maxSeq++
		history = append(history, engine.Event{
			Kind:    engine.EventStageFailed,
			NodeID:  node,
			Message: reason,
			Seq:     maxSeq,
		})
	}
	maxSeq++
	history = append(history, engine.Event{
		Kind:    engine.EventPipelineFailed,
		Message: reason,
		Seq:     maxSeq,
	})
	return history
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
