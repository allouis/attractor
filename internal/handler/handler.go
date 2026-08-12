// Package handler implements the built-in node handler set: start, exit,
// conditional, codergen, wait.human.
package handler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/interviewer"
	"github.com/allouis/attractor/internal/runstore"
)

// Start is a no-op handler used for `start` nodes (spec §4.3).
type Start struct{}

// Execute returns a SUCCESS outcome immediately.
func (Start) Execute(env engine.HandlerEnv) engine.Outcome {
	return engine.Outcome{Status: engine.StatusSuccess, Notes: "start"}
}

// Exit is a no-op handler used for `exit` terminal nodes (spec §4.4).
// The engine performs goal-gate enforcement around it.
type Exit struct{}

// Execute returns a SUCCESS outcome immediately.
func (Exit) Execute(env engine.HandlerEnv) engine.Outcome {
	return engine.Outcome{Status: engine.StatusSuccess, Notes: "exit"}
}

// Conditional is a pass-through handler used for diamond-shaped routing
// nodes (spec §4.7). The actual condition evaluation lives in the
// engine's edge selection.
type Conditional struct{}

// Execute returns SUCCESS so the engine can resolve outgoing-edge
// conditions against current context.
func (Conditional) Execute(env engine.HandlerEnv) engine.Outcome {
	return engine.Outcome{Status: engine.StatusSuccess, Notes: "conditional"}
}

// Codergen is the LLM-driven node handler (spec §4.5).
type Codergen struct {
	Backend backend.CodergenBackend
}

// Execute resolves the prompt, dispatches to the backend, writes the
// prompt/response artifacts under the run directory, and returns the
// outcome. Under non-`full` fidelity, env.Preamble is prepended so the
// stage prompt carries enough run state to ground a fresh LLM session.
//
// Outcome resolution follows spec §4.5 and Appendix C:
//  1. If the backend itself produced an Outcome, use it verbatim.
//  2. Else, if the agent wrote `{stage_dir}/status.json` during its
//     turn (the status-file contract), parse it and use it.
//  3. Else, synthesise SUCCESS from the response text.
func (h Codergen) Execute(env engine.HandlerEnv) engine.Outcome {
	prompt := env.Node.Prompt()
	if prompt == "" {
		prompt = env.Node.Label()
	}
	// Interpolate $context.* / $goal from the live context (spec §4.5)
	// before grounding the prompt with the preamble.
	expanded, err := env.Context.Expand(prompt)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: err.Error()}
	}
	prompt = expanded
	if env.Preamble != "" {
		prompt = env.Preamble + "\n\n---\n\n" + prompt
	}

	// All artifacts are written through the stage store, which is rooted at
	// the stage dir and cannot write outside it. A nil store means a
	// no-persistence run: run the backend but skip all file I/O.
	stage := env.Stage
	if stage == nil {
		return h.runNoPersistence(env, prompt)
	}
	if err := stage.MkdirAll(); err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("mkdir stage dir: %v", err)}
	}
	// Resolve the status-file contract's {stage_dir} placeholder to the real
	// path before the prompt reaches the agent. Agents must never guess it:
	// the review-core synth guessed `/status.json`, its FAIL verdict was
	// never read, and the run shipped a failed review as a pass (2026-08-12,
	// run a9dd311b).
	prompt = strings.ReplaceAll(prompt, "{stage_dir}", stage.Root())
	// Wipe any status.json left behind by a previous attempt so the
	// self-report check below only fires for the agent's own writes.
	_ = stage.Remove("status.json")
	if err := stage.Write("prompt.md", []byte(prompt)); err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("write prompt: %v", err)}
	}

	if h.Backend == nil {
		response := "[simulated] " + env.Node.ID
		_ = stage.Write("response.md", []byte(response))
		return engine.Outcome{
			Status:         engine.StatusSuccess,
			Notes:          "Stage completed (simulated): " + env.Node.ID,
			ContextUpdates: applyDefaults(nil, env.Node, response),
		}
	}

	// An agent whose cwd is a work dir (a repo, via the node's `cwd`
	// attribute, or attractor's own cwd when unset) may write its
	// status.json there rather than to the stage dir the contract expects.
	// Probe that location so a status.json which appears during the turn is
	// relocated into the stage dir afterwards — otherwise the engine misses
	// the agent's self-report (silently defaulting to SUCCESS) and the file
	// leaks into the work dir / a tracked repo.
	workStatus, workStatusExisted := leakedStatusProbe(env)

	result, err := h.Backend.Run(env, prompt)
	relocateLeakedStatus(stage, workStatus, workStatusExisted)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: err.Error()}
	}
	if result.Outcome != nil {
		if result.ResponseText != "" {
			_ = stage.Write("response.md", []byte(result.ResponseText))
		}
		return *result.Outcome
	}
	response := result.ResponseText
	_ = stage.Write("response.md", []byte(response))

	requireStatus := env.Node.Bool("require_status")
	oc, ok := readAgentStatus(stage)
	if !ok && requireStatus {
		// A verdict node's agent may land its final status.json write shortly
		// after the backend turn returns — in-guest that write races the last
		// flush through the 9p mount by ~2s, so an honest verdict would be
		// scored as missing. Poll a bounded window; a status that appears
		// within it is used normally (including a FAIL). Only require_status
		// nodes pay this cost — every other node keeps the single immediate
		// check below, so there is no universal slowdown.
		oc, ok = pollAgentStatus(stage, requireStatusGraceWindow, requireStatusPollInterval)
	}
	if ok {
		// Status-file contract: agent self-reported its outcome.
		// Merge in the routing baggage so the engine still sees last_stage
		// and last_response in context.
		oc.ContextUpdates = applyDefaults(oc.ContextUpdates, env.Node, response)
		return oc
	}

	// A verdict node (require_status=true) whose status never arrived must
	// fail loud, not default to success — defaulting is how a lost FAIL
	// verdict passes a review gate.
	if requireStatus {
		return engine.Outcome{
			Status:         engine.StatusFail,
			FailureReason:  requireStatusMissReason(stage),
			ContextUpdates: applyDefaults(nil, env.Node, response),
		}
	}

	return engine.Outcome{
		Status:         engine.StatusSuccess,
		Notes:          "Stage completed: " + env.Node.ID,
		ContextUpdates: applyDefaults(nil, env.Node, response),
	}
}

// runNoPersistence runs the backend without any artifact file I/O, for a
// run that has no logs root (env.Stage == nil).
func (h Codergen) runNoPersistence(env engine.HandlerEnv, prompt string) engine.Outcome {
	if h.Backend == nil {
		response := "[simulated] " + env.Node.ID
		return engine.Outcome{Status: engine.StatusSuccess, Notes: "Stage completed (simulated): " + env.Node.ID, ContextUpdates: applyDefaults(nil, env.Node, response)}
	}
	result, err := h.Backend.Run(env, prompt)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: err.Error()}
	}
	if result.Outcome != nil {
		return *result.Outcome
	}
	return engine.Outcome{Status: engine.StatusSuccess, Notes: "Stage completed: " + env.Node.ID, ContextUpdates: applyDefaults(nil, env.Node, result.ResponseText)}
}

// applyDefaults fills the routing baggage (last_stage, last_response) and,
// when the node sets output_key, the node's full untruncated response — all
// without clobbering keys the agent already set via its status.json.
func applyDefaults(updates map[string]string, node *graph.Node, response string) map[string]string {
	if updates == nil {
		updates = map[string]string{}
	}
	setIfAbsent(updates, "last_stage", node.ID)
	setIfAbsent(updates, "last_response", truncate(response, 200))
	if key := node.OutputKey(); key != "" {
		setIfAbsent(updates, key, response)
	}
	return updates
}

func setIfAbsent(m map[string]string, key, value string) {
	if _, ok := m[key]; !ok {
		m[key] = value
	}
}

// leakedStatusProbe returns the path where the agent might write its
// status.json instead of the stage dir — its work dir (env.Cwd, falling
// back to attractor's cwd) — and whether a file already exists there. An
// empty work dir, or one whose status.json coincides with the stage dir,
// yields an empty path (nothing to relocate).
func leakedStatusProbe(env engine.HandlerEnv) (path string, existed bool) {
	workDir := env.Cwd
	if workDir == "" {
		workDir, _ = os.Getwd() // runstore:allow read (not write) the ambient cwd to locate a subprocess's leaked status.json
	}
	if workDir == "" {
		return "", false
	}
	ws := filepath.Join(workDir, "status.json")
	// If the work dir IS the stage dir, the agent writing there is correct.
	if env.Stage != nil && ws == filepath.Join(env.Stage.Root(), "status.json") {
		return "", false
	}
	_, err := os.Stat(ws)
	return ws, err == nil
}

// relocateLeakedStatus moves a status.json the agent wrote into its work
// dir (cwd) into the stage dir, so the status-file contract reads it and
// it does not leak into the work dir (e.g. a tracked repo). It acts only
// on a file that APPEARED during the turn: a status.json that predated the
// turn is left untouched (it is not this agent's self-report). If the
// agent also wrote to the stage dir, that copy wins and the work-dir file
// is merely cleaned up.
func relocateLeakedStatus(stage *runstore.Dir, workStatus string, existedBefore bool) {
	if workStatus == "" || existedBefore {
		return
	}
	data, err := os.ReadFile(workStatus)
	if err != nil {
		return // agent wrote nothing to its cwd
	}
	_ = os.Remove(workStatus) // runstore:allow remove the agent's leaked file from the work dir (cwd), not an artifact write
	if stage.Exists("status.json") {
		return // agent also wrote the stage-dir copy; keep that one
	}
	_ = stage.Write("status.json", data)
}

// requireStatusGraceWindow / requireStatusPollInterval bound how long a
// require_status node waits for the agent's status.json after the backend turn
// ends, before failing as missing. In-guest the agent's final write can land
// ~2s after the turn returns (it races the last flush through the 9p mount);
// the window covers that without slowing any other node. Package vars so tests
// can shrink them.
var (
	requireStatusGraceWindow  = 5 * time.Second
	requireStatusPollInterval = 200 * time.Millisecond
)

// pollAgentStatus retries readAgentStatus until it succeeds or the window
// elapses. A status that appears within the window is used verbatim (including
// a FAIL verdict); a status that never arrives returns (zero, false) so the
// caller emits the require_status-miss failure.
func pollAgentStatus(stage *runstore.Dir, window, interval time.Duration) (engine.Outcome, bool) {
	deadline := time.Now().Add(window)
	for {
		time.Sleep(interval)
		if oc, ok := readAgentStatus(stage); ok {
			return oc, true
		}
		if !time.Now().Before(deadline) {
			return engine.Outcome{}, false
		}
	}
}

// requireStatusMissReason is the failure_reason a require_status node emits
// when the agent's status.json never arrived within the grace window. It marks
// a MACHINERY failure of the verdict harness (the verdict was never written) —
// distinct from a FAIL verdict the agent authored. manager_loop keys on this
// signature (via isRequireStatusMiss) so a harness miss is not fed to a fix
// agent as if it were a review finding.
func requireStatusMissReason(stage *runstore.Dir) string {
	return "agent wrote no " + filepath.Join(stage.Root(), "status.json") + " (require_status node)"
}

// isRequireStatusMiss reports whether a failure_reason carries the
// require_status machinery-miss signature (as opposed to an agent-authored
// verdict). The signature survives wrapping ("manager_loop: child failed — …"),
// so it holds when the reason has bubbled up through nested manager loops.
func isRequireStatusMiss(reason string) bool {
	return strings.Contains(reason, "status.json (require_status node)")
}

// readAgentStatus reads the agent-authored status.json if present and
// well-formed. Returns (outcome, true) on success or (zero, false)
// otherwise so the handler falls through to its default SUCCESS path.
func readAgentStatus(stage *runstore.Dir) (engine.Outcome, bool) {
	data, err := stage.Read("status.json")
	if err != nil {
		return engine.Outcome{}, false
	}
	var oc engine.Outcome
	if err := json.Unmarshal(data, &oc); err != nil {
		return engine.Outcome{}, false
	}
	oc.Status = engine.ParseStatus(oc.StatusString)
	if oc.Status == engine.StatusUnknown {
		return engine.Outcome{}, false
	}
	return oc, true
}

// WaitHuman blocks on the configured interviewer to obtain an edge
// label selection (spec §4.6).
type WaitHuman struct {
	Interviewer interviewer.Interviewer
}

// Execute resolves the outgoing edges into a multiple-choice question
// and dispatches it to the interviewer. The selected edge's label
// becomes the outcome's preferred_label.
func (h WaitHuman) Execute(env engine.HandlerEnv) engine.Outcome {
	if h.Interviewer == nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "wait.human: no interviewer configured"}
	}
	edges := env.Graph.OutgoingEdges(env.Node.ID)
	if len(edges) == 0 {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "wait.human: no outgoing edges"}
	}
	options := make([]interviewer.Option, 0, len(edges))
	for _, e := range edges {
		label := e.Label()
		if label == "" {
			label = e.To
		}
		key := acceleratorFor(label)
		options = append(options, interviewer.Option{Key: key, Label: label})
	}
	text := env.Node.Label()
	if text == "" || text == env.Node.ID {
		text = "Select an option:"
	}
	question := interviewer.Question{
		ID:      env.Node.ID + "-" + env.RunID,
		Text:    text,
		Type:    interviewer.QuestionMultipleChoice,
		Options: options,
		Stage:   env.Node.ID,
		Timeout: parseGateTimeout(env.Node.Attrs["human.timeout_seconds"]),
	}
	if env.Emit != nil {
		opts := make([]engine.InterviewOption, 0, len(options))
		for _, o := range options {
			opts = append(opts, engine.InterviewOption{Key: o.Key, Label: o.Label})
		}
		env.Emit(engine.Event{
			Kind:       engine.EventInterviewStarted,
			NodeID:     env.Node.ID,
			QuestionID: question.ID,
			Message:    text,
			Question: &engine.InterviewQuestion{
				Text:    text,
				Type:    "multiple_choice",
				Stage:   env.Node.ID,
				Options: opts,
			},
		})
	}
	answer, err := h.Interviewer.Ask(question)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "wait.human: " + err.Error()}
	}
	if env.Emit != nil {
		ev := engine.Event{
			Kind:       engine.EventInterviewAnswered,
			NodeID:     env.Node.ID,
			QuestionID: question.ID,
			Message:    answer.Text,
		}
		// Record the human's choice on the event so a replayed/reloaded run can
		// show the chosen option label and note without a live-capture side
		// channel (R2 gate turns): the answered event was the one lossy point.
		// Only a genuine choice carries a label; a timeout/skip keeps the bare
		// event. Additive — consumers that ignore detail are unaffected.
		if answer.SelectedOption != nil {
			ev.Detail = map[string]string{
				"label": selectChoice(answer, options).Label,
				"note":  answer.Text,
			}
		}
		env.Emit(ev)
	}
	switch answer.Value {
	case interviewer.AnswerTimeout:
		return h.onTimeout(env, question.ID, edges, options)
	case interviewer.AnswerSkipped:
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "wait.human: skipped"}
	}
	selected := selectChoice(answer, options)
	return engine.Outcome{
		Status:         engine.StatusSuccess,
		PreferredLabel: selected.Label,
		ContextUpdates: map[string]string{
			"human.gate.selected": selected.Key,
			"human.gate.label":    selected.Label,
			// The optional note the human attached to the answer, surfaced to
			// the revisited node's prompt as reviewer feedback ($context.human.note).
			// The engine seeds this key to "" at run start, so a first visit
			// before any answer still resolves; each answer overwrites it.
			"human.note": answer.Text,
		},
	}
}

// onTimeout resolves a wait.human timeout (spec §6.5): if the node sets
// `human.default_choice` to one of the outgoing edges (by key, label, or
// target), that edge is selected as if a human had chosen it; otherwise the
// node retries. Emits an InterviewTimeout event either way (spec §9.6).
func (h WaitHuman) onTimeout(env engine.HandlerEnv, questionID string, edges []*graph.Edge, options []interviewer.Option) engine.Outcome {
	emit := func(msg string) {
		if env.Emit != nil {
			env.Emit(engine.Event{Kind: engine.EventInterviewTimeout, NodeID: env.Node.ID, QuestionID: questionID, Message: msg})
		}
	}
	dc := env.Node.Attrs["human.default_choice"]
	if dc != "" {
		if opt, ok := defaultChoiceOption(dc, edges, options); ok {
			emit("default_choice=" + dc)
			return engine.Outcome{
				Status:         engine.StatusSuccess,
				PreferredLabel: opt.Label,
				ContextUpdates: map[string]string{
					"human.gate.selected": opt.Key,
					"human.gate.label":    opt.Label,
					"human.gate.timeout":  "true",
				},
			}
		}
	}
	emit("no default_choice")
	return engine.Outcome{Status: engine.StatusRetry, FailureReason: "wait.human: timeout, no default_choice"}
}

// parseGateTimeout reads a human-gate timeout from the node attribute,
// accepting either a plain number of seconds (spec's timeout_seconds) or a
// Go duration string ("30s", "5m"). Zero/blank means no timeout.
func parseGateTimeout(s string) time.Duration {
	if s == "" {
		return 0
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		if f <= 0 {
			return 0
		}
		return time.Duration(f * float64(time.Second))
	}
	if d, ok := graph.ParseDuration(s); ok {
		return d
	}
	return 0
}

// defaultChoiceOption finds the option a `human.default_choice` names,
// matching by accelerator key, label, or edge target.
func defaultChoiceOption(dc string, edges []*graph.Edge, options []interviewer.Option) (interviewer.Option, bool) {
	for i, e := range edges {
		if i >= len(options) {
			break
		}
		if dc == options[i].Key || dc == options[i].Label || dc == e.To {
			return options[i], true
		}
	}
	return interviewer.Option{}, false
}

func selectChoice(a interviewer.Answer, options []interviewer.Option) interviewer.Option {
	if a.SelectedOption != nil {
		for _, o := range options {
			if o.Key == a.SelectedOption.Key {
				return o
			}
		}
		return *a.SelectedOption
	}
	if a.Text != "" {
		for _, o := range options {
			if o.Key == a.Text || o.Label == a.Text {
				return o
			}
		}
	}
	if a.Value == interviewer.AnswerYes {
		for _, o := range options {
			if o.Key == "Y" || o.Key == "y" {
				return o
			}
		}
	}
	return options[0]
}

func acceleratorFor(label string) string {
	if len(label) == 0 {
		return ""
	}
	// `[K] text`
	if label[0] == '[' {
		for i := 1; i < len(label); i++ {
			if label[i] == ']' {
				return label[1:i]
			}
		}
	}
	// `K) text` / `K - text`
	if len(label) >= 2 {
		sep := label[1]
		if sep == ')' || sep == '-' {
			return string(label[0])
		}
		if sep == ' ' && len(label) >= 3 && (label[2] == '-' || label[2] == '.') {
			return string(label[0])
		}
	}
	return string(label[0])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
