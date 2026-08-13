package engine

import (
	"fmt"

	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

// Handler executes a single node and returns its Outcome. Handlers are
// stateless or guard their own internal state; the engine never invokes
// a handler concurrently for the same node.
type Handler interface {
	Execute(env HandlerEnv) Outcome
}

// HandlerEnv bundles everything a handler needs to do its work. Passing
// a struct keeps the interface stable as new optional inputs are added.
type HandlerEnv struct {
	Node  *graph.Node
	Graph *graph.Graph
	// Stage is the write seam for this node's stage directory. Handlers
	// and backends persist prompt/response/status/tool-call artifacts
	// through it so they cannot write outside the stage dir (e.g. into the
	// process cwd / a user repo). Nil only for no-persistence runs.
	Stage    *runstore.Dir
	Context  *Context
	LogsRoot string
	RunID    string
	Emit     func(Event)
	Registry *Registry

	// Fidelity is the resolved context-fidelity mode (spec §5.4) for
	// the incoming traversal. Codergen handlers prepend Preamble to the
	// node's prompt when fidelity is not `full`.
	Fidelity FidelityMode
	// ThreadID identifies the LLM session to reuse under `full`
	// fidelity. Codergen backends with session reuse use this as the
	// thread key.
	ThreadID string
	// Preamble is the synthesised context carry-over text. Empty under
	// `full` fidelity.
	Preamble string
	// Cwd is the working directory the handler should use for any
	// subprocess (tool command, codergen backend). Resolved from the
	// node's `cwd` attribute, falling back to the graph-level `cwd`,
	// then empty. Empty leaves the subprocess at attractor's cwd for
	// codergen and at the stage dir for tool handlers.
	Cwd string
	// ExecuteNode runs another node through the engine's ONE executor —
	// visit/attempt counting, retry policy, panic recovery, span-dir
	// storage, and stage events all included. Composite handlers
	// (parallel) fan out through this instead of invoking handlers
	// directly, so a branch is never a second execution dialect.
	ExecuteNode func(nodeID string, ctx *Context) Outcome
}

// Registry maps handler types to handler instances (spec §4.2).
type Registry struct {
	handlers map[string]Handler
	defaultH Handler
}

// NewRegistry returns an empty registry. Callers register handlers
// explicitly; there is no implicit default for safety.
func NewRegistry() *Registry { return &Registry{handlers: map[string]Handler{}} }

// Register associates a handler with a type string. Subsequent
// Register calls for the same type replace the previous handler.
func (r *Registry) Register(typeStr string, h Handler) {
	r.handlers[typeStr] = h
}

// SetDefault installs a fallback handler used when no registered type
// matches.
func (r *Registry) SetDefault(h Handler) { r.defaultH = h }

// Resolve picks the handler for a node per spec §4.2: explicit `type`
// attribute first, then shape-based resolution via TypeFromShape, then
// the registered default.
func (r *Registry) Resolve(n *graph.Node) (Handler, error) {
	if t := n.Attrs["type"]; t != "" {
		if h, ok := r.handlers[t]; ok {
			return h, nil
		}
		if r.defaultH != nil {
			return r.defaultH, nil
		}
		return nil, fmt.Errorf("no handler registered for type %q", t)
	}
	if h, ok := r.handlers[graph.TypeFromShape(n.Shape())]; ok {
		return h, nil
	}
	if r.defaultH != nil {
		return r.defaultH, nil
	}
	return nil, fmt.Errorf("no handler registered for shape %q on node %q", n.Shape(), n.ID)
}
