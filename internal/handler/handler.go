// Package handler implements the built-in node handler set: start, exit,
// conditional, codergen, wait.human.
package handler

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/fabro/attractor/internal/backend"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/interviewer"
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
// outcome.
func (h Codergen) Execute(env engine.HandlerEnv) engine.Outcome {
	prompt := env.Node.Prompt()
	if prompt == "" {
		prompt = env.Node.Label()
	}

	stageDir := filepath.Join(env.LogsRoot, env.Node.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("mkdir stage dir: %v", err)}
	}
	if err := os.WriteFile(filepath.Join(stageDir, "prompt.md"), []byte(prompt), 0o644); err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: fmt.Sprintf("write prompt: %v", err)}
	}

	if h.Backend == nil {
		response := "[simulated] " + env.Node.ID
		_ = os.WriteFile(filepath.Join(stageDir, "response.md"), []byte(response), 0o644)
		updates := map[string]string{
			"last_stage":    env.Node.ID,
			"last_response": truncate(response, 200),
		}
		return engine.Outcome{
			Status:         engine.StatusSuccess,
			Notes:          "Stage completed (simulated): " + env.Node.ID,
			ContextUpdates: updates,
		}
	}

	result, err := h.Backend.Run(env, prompt)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: err.Error()}
	}
	if result.Outcome != nil {
		_ = writeIfNonEmpty(filepath.Join(stageDir, "response.md"), result.ResponseText)
		return *result.Outcome
	}
	response := result.ResponseText
	_ = os.WriteFile(filepath.Join(stageDir, "response.md"), []byte(response), 0o644)
	updates := map[string]string{
		"last_stage":    env.Node.ID,
		"last_response": truncate(response, 200),
	}
	return engine.Outcome{
		Status:         engine.StatusSuccess,
		Notes:          "Stage completed: " + env.Node.ID,
		ContextUpdates: updates,
	}
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
	}
	if env.Emit != nil {
		env.Emit(engine.Event{
			Kind:       engine.EventInterviewStarted,
			NodeID:     env.Node.ID,
			QuestionID: question.ID,
			Message:    text,
		})
	}
	answer, err := h.Interviewer.Ask(question)
	if err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "wait.human: " + err.Error()}
	}
	if env.Emit != nil {
		env.Emit(engine.Event{
			Kind:       engine.EventInterviewAnswered,
			NodeID:     env.Node.ID,
			QuestionID: question.ID,
			Message:    answer.Text,
		})
	}
	switch answer.Value {
	case interviewer.AnswerTimeout:
		return engine.Outcome{Status: engine.StatusRetry, FailureReason: "wait.human: timeout"}
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
		},
	}
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

func writeIfNonEmpty(path, content string) error {
	if content == "" {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
