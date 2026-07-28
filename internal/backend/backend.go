// Package backend defines the CodergenBackend interface (Attractor
// §4.5) and the small types backends share. Concrete implementations
// live in sub-packages (fake, claudecode).
package backend

import (
	"github.com/allouis/attractor/internal/engine"
)

// Result is what a CodergenBackend returns. Either ResponseText is set
// (the engine wraps it into a SUCCESS outcome) or Outcome is set (the
// engine uses the outcome verbatim).
type Result struct {
	ResponseText string
	Outcome      *engine.Outcome
}

// CodergenBackend is the interface the codergen handler dispatches to
// for each LLM-driven stage.
type CodergenBackend interface {
	// Run executes the stage and returns the resulting text or outcome.
	// Implementations may emit Attractor events via env.Emit and read
	// shared state via env.Context.
	Run(env engine.HandlerEnv, prompt string) (Result, error)
}

// Func is an adapter so plain functions satisfy CodergenBackend without
// extra boilerplate.
type Func func(env engine.HandlerEnv, prompt string) (Result, error)

// Run dispatches to the underlying function.
func (f Func) Run(env engine.HandlerEnv, prompt string) (Result, error) {
	return f(env, prompt)
}
