package server

import (
	"net/http"

	"github.com/allouis/attractor/internal/graph"
)

// nodeDetail is everything knowable about a node before it runs, served from
// the run's prepared graph (ui-run-view-v3 P1): its handler type, the resolved
// prompt text (@file refs already inlined by setup.Prepare), timeout, retry
// policy, llm provider/model, and the edges in and out with their conditions
// and labels. Available from run start for every node, so the inspector can
// show a yet-to-run node's prompt.
type nodeDetail struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Prompt      string     `json:"prompt,omitempty"`
	Timeout     string     `json:"timeout,omitempty"`
	Retry       *retryView `json:"retry,omitempty"`
	LLMProvider string     `json:"llm_provider,omitempty"`
	LLMModel    string     `json:"llm_model,omitempty"`
	Incoming    []edgeView `json:"incoming"`
	Outgoing    []edgeView `json:"outgoing"`
}

// retryView is a node's resolved retry allowance: the number of retries the
// engine grants beyond the first attempt (node max_retries, else the graph
// default).
type retryView struct {
	MaxRetries int `json:"max_retries"`
}

// edgeView is one directed edge with the routing metadata the inspector shows.
type edgeView struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
	Label     string `json:"label,omitempty"`
}

// getRunNode serves GET /pipelines/{id}/nodes/{node}.
func (s *Server) getRunNode(w http.ResponseWriter, r *http.Request) {
	run, ok := s.registry.Get(r.PathValue("id"))
	if !ok {
		writeJSONError(w, http.StatusNotFound, "run not found")
		return
	}
	g := run.resolvedGraph()
	if g == nil {
		writeJSONError(w, http.StatusNotFound, "graph unavailable")
		return
	}
	nodeID := r.PathValue("node")
	node, ok := g.Nodes[nodeID]
	if !ok {
		writeJSONError(w, http.StatusNotFound, "node not found")
		return
	}
	writeJSON(w, http.StatusOK, nodeDetailOf(g, node))
}

// nodeDetailOf builds the served detail for one node of the graph.
func nodeDetailOf(g *graph.Graph, node *graph.Node) nodeDetail {
	d := nodeDetail{
		ID:          node.ID,
		Type:        node.Type(),
		Prompt:      node.Prompt(),
		Timeout:     node.Attrs["timeout"],
		LLMProvider: node.Attrs["llm_provider"],
		LLMModel:    node.Attrs["llm_model"],
		Incoming:    edgeViews(g.IncomingEdges(node.ID)),
		Outgoing:    edgeViews(g.OutgoingEdges(node.ID)),
	}
	// The engine grants retries only when max_retries resolves > 0 (node attr,
	// else the graph default). Mirror that resolution so the inspector shows
	// what the run will actually allow.
	max := node.Int("max_retries", g.IntAttr("default_max_retries", g.IntAttr("default_max_retry", 0)))
	if max > 0 {
		d.Retry = &retryView{MaxRetries: max}
	}
	return d
}

// edgeViews maps graph edges to their served form, preserving order.
func edgeViews(edges []*graph.Edge) []edgeView {
	out := make([]edgeView, 0, len(edges))
	for _, e := range edges {
		out = append(out, edgeView{From: e.From, To: e.To, Condition: e.Condition(), Label: e.Label()})
	}
	return out
}
