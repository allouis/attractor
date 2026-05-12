package engine

import (
	"fmt"

	"github.com/fabro/attractor/internal/artifact"
	"github.com/fabro/attractor/internal/graph"
)

// Handler executes a single node and returns its Outcome. Handlers are
// stateless or guard their own internal state; the engine never invokes
// a handler concurrently for the same node.
type Handler interface {
	Execute(env HandlerEnv) Outcome
}

// HandlerEnv bundles everything a handler needs to do its work. Passing
// a struct keeps the interface stable as new optional inputs are added
// (event channels, ingest URL, etc).
type HandlerEnv struct {
	Node      *graph.Node
	Graph     *graph.Graph
	Context   *Context
	LogsRoot  string
	RunID     string
	Emit      func(Event)
	Registry  *Registry
	Artifacts *artifact.Store
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
