// Package router provides a CodergenBackend that dispatches each
// codergen node to a backend chosen from the provider config
// (docs/provider-config.md). Nodes declare intent (llm_provider / llm_model);
// the router resolves that to a provider, constructs the matching
// backend once per provider (cached, so ACP backends keep their
// per-thread session maps), and injects llm_model via the provider's
// model_env.
package router

import (
	"fmt"
	"sync"

	"github.com/allouis/attractor/internal/backend"
	acpbackend "github.com/allouis/attractor/internal/backend/acp"
	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
)

// Router implements backend.CodergenBackend by routing per node.
type Router struct {
	cfg config.Config
	// strict fails loud on a model-less codergen node instead of
	// simulating it. Set when the run clearly intends real agents (a
	// --stylesheet was supplied or a default_provider is configured); off
	// for a bare dev run, which may legitimately simulate everything.
	strict bool

	mu    sync.Mutex
	cache map[string]backend.CodergenBackend
}

// New returns a Router that routes codergen nodes through cfg. A
// model-less node simulates (lenient — bare dev runs).
func New(cfg config.Config) *Router {
	return &Router{cfg: cfg, cache: map[string]backend.CodergenBackend{}}
}

// NewStrict returns a Router that fails loud (rather than simulating) on a
// codergen node that resolves to no model — for runs that intend real
// agents.
func NewStrict(cfg config.Config, strict bool) *Router {
	return &Router{cfg: cfg, strict: strict, cache: map[string]backend.CodergenBackend{}}
}

// Run satisfies backend.CodergenBackend: pick the backend for this node
// and delegate.
func (r *Router) Run(env engine.HandlerEnv, prompt string) (backend.Result, error) {
	be, err := r.backendFor(env.Node)
	if err != nil {
		return backend.Result{}, err
	}
	return be.Run(env, prompt)
}

// backendFor resolves and caches the backend for a node's provider.
func (r *Router) backendFor(n *graph.Node) (backend.CodergenBackend, error) {
	name := r.cfg.ResolveProvider(n)
	// Fail loud instead of silently simulating an agent node. An empty
	// name means no llm_provider, no recognised llm_model, and no
	// default_provider resolved. In strict mode (a --stylesheet was passed
	// or a default_provider is configured, so the run clearly intends real
	// agents) that is almost always a missing `--stylesheet` or a forgotten
	// `llm_model` — running it as [simulated] while its siblings hit real
	// agents is the dangerous, invisible failure.
	if name == "" && r.strict {
		return nil, fmt.Errorf("router: node %q has no model — set llm_model on the node, tag it with a class matched by your --stylesheet, or set default_provider; refusing to silently simulate an agent node", n.ID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if be, ok := r.cache[name]; ok {
		return be, nil
	}
	be, err := r.construct(name)
	if err != nil {
		return nil, err
	}
	r.cache[name] = be
	return be, nil
}

// construct builds the backend for a resolved provider name. An empty
// name (no llm_provider, unrecognised llm_model, no default_provider)
// means "no provider selected" and falls back to simulation so a bare
// run with no config still works.
func (r *Router) construct(name string) (backend.CodergenBackend, error) {
	if name == "" {
		return simulation(), nil
	}
	p, ok := r.cfg.Providers[name]
	if !ok {
		return nil, fmt.Errorf("router: provider %q not configured in ~/.attractor/config.json", name)
	}
	switch p.Backend {
	case "acp":
		return &acpbackend.Backend{Command: p.Command, ModelEnv: p.ModelEnv}, nil
	case "simulation":
		return simulation(), nil
	case "":
		return nil, fmt.Errorf("router: provider %q sets no backend", name)
	default:
		return nil, fmt.Errorf("router: provider %q has unknown backend %q", name, p.Backend)
	}
}

// simulation is the no-agent backend: it echoes a simulated marker,
// matching the codergen handler's nil-backend path.
func simulation() backend.CodergenBackend {
	return backend.Func(func(env engine.HandlerEnv, _ string) (backend.Result, error) {
		return backend.Result{ResponseText: "[simulated] " + env.Node.ID}, nil
	})
}
