// Package fake provides a deterministic CodergenBackend used by tests.
// Responses and outcomes are configured per node ID; an optional sequence
// drives retry-aware scenarios where the first N attempts fail before
// success.
package fake

import (
	"fmt"
	"sync"

	"github.com/fabro/attractor/internal/backend"
	"github.com/fabro/attractor/internal/engine"
)

// Step is a single canned reply for a node. Either Outcome or Text wins.
type Step struct {
	Text    string
	Outcome *engine.Outcome
	Err     error
}

// Backend records each Run call and yields canned Steps in order for the
// node ID specified by the call's HandlerEnv.
type Backend struct {
	mu      sync.Mutex
	steps   map[string][]Step
	calls   map[string]int
	callLog []Call
}

// Call is a recorded invocation of Run, useful for assertion in tests.
type Call struct {
	NodeID string
	Prompt string
	Stage  string
}

// New returns an empty Backend.
func New() *Backend {
	return &Backend{
		steps: map[string][]Step{},
		calls: map[string]int{},
	}
}

// SetSequence configures the steps for a node. The Nth call for the node
// returns steps[N-1]; once the list is exhausted the final element is
// repeated.
func (b *Backend) SetSequence(nodeID string, steps ...Step) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.steps[nodeID] = steps
}

// SetText is a convenience for the common "single SUCCESS reply" case.
func (b *Backend) SetText(nodeID, text string) {
	b.SetSequence(nodeID, Step{Text: text})
}

// Calls returns a snapshot of recorded Run invocations in order.
func (b *Backend) Calls() []Call {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]Call{}, b.callLog...)
	return out
}

// CallCount returns the number of times Run has been invoked for a node.
func (b *Backend) CallCount(nodeID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls[nodeID]
}

// Run satisfies CodergenBackend. Unknown nodes default to a SUCCESS
// outcome with synthetic text so tests can run pipelines that haven't
// fully wired every step.
func (b *Backend) Run(env engine.HandlerEnv, prompt string) (backend.Result, error) {
	b.mu.Lock()
	id := env.Node.ID
	idx := b.calls[id]
	b.calls[id] = idx + 1
	b.callLog = append(b.callLog, Call{NodeID: id, Prompt: prompt, Stage: id})
	steps := b.steps[id]
	b.mu.Unlock()

	if len(steps) == 0 {
		return backend.Result{ResponseText: fmt.Sprintf("[fake] %s -> ok", id)}, nil
	}
	step := steps[len(steps)-1]
	if idx < len(steps) {
		step = steps[idx]
	}
	if step.Err != nil {
		return backend.Result{}, step.Err
	}
	if step.Outcome != nil {
		cp := *step.Outcome
		return backend.Result{Outcome: &cp}, nil
	}
	return backend.Result{ResponseText: step.Text}, nil
}
