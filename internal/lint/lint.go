// Package lint implements Attractor's validation rules (spec §7). Each
// rule inspects a built Graph and emits zero or more Diagnostic records.
// The engine rejects pipelines that produce any ERROR-severity entry.
package lint

import (
	"fmt"
	"strings"

	"github.com/fabro/attractor/internal/condition"
	"github.com/fabro/attractor/internal/graph"
)

// validFidelityModes mirrors engine.FidelityMode constants. Duplicated
// here because lint runs before engine wiring and the engine package
// already imports lint (no cycles).
var validFidelityModes = map[string]struct{}{
	"full": {}, "truncate": {}, "compact": {},
	"summary:low": {}, "summary:medium": {}, "summary:high": {},
}

// Severity classifies a diagnostic for the engine's
// validate-or-raise decision.
type Severity int

const (
	Info    Severity = iota // informational note
	Warning                 // execution continues
	Error                   // pipeline will not execute
)

func (s Severity) String() string {
	switch s {
	case Info:
		return "info"
	case Warning:
		return "warning"
	case Error:
		return "error"
	}
	return "unknown"
}

// Diagnostic is a single lint finding produced by a Rule.
type Diagnostic struct {
	Rule     string
	Severity Severity
	Message  string
	NodeID   string
	EdgeFrom string
	EdgeTo   string
	Fix      string
}

// Rule is the lint-rule interface. Implementations should be stateless.
type Rule interface {
	Name() string
	Apply(g *graph.Graph) []Diagnostic
}

// Validate runs the supplied rules in order and returns all diagnostics.
// The default ruleset is BuiltIn(); callers may append custom rules.
func Validate(g *graph.Graph, extra ...Rule) []Diagnostic {
	rules := BuiltIn()
	rules = append(rules, extra...)
	var out []Diagnostic
	for _, r := range rules {
		out = append(out, r.Apply(g)...)
	}
	return out
}

// ValidateOrError runs Validate and returns a synthesised error if any
// ERROR-severity diagnostics exist. Warnings/info are still returned
// alongside the (possibly nil) error so callers can present them.
func ValidateOrError(g *graph.Graph, extra ...Rule) ([]Diagnostic, error) {
	diags := Validate(g, extra...)
	var errs []string
	for _, d := range diags {
		if d.Severity == Error {
			errs = append(errs, fmt.Sprintf("%s: %s", d.Rule, d.Message))
		}
	}
	if len(errs) > 0 {
		return diags, fmt.Errorf("pipeline validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return diags, nil
}

// BuiltIn returns the canonical lint ruleset shipped with the engine.
func BuiltIn() []Rule {
	return []Rule{
		StartNodeRule{},
		TerminalNodeRule{},
		StartNoIncomingRule{},
		ExitNoOutgoingRule{},
		EdgeTargetExistsRule{},
		ReachabilityRule{},
		ConditionSyntaxRule{},
		PromptOnLLMNodesRule{},
		TypeKnownRule{},
		CodergenTypeKnownRule{},
		ACPCommandRule{},
		RetryTargetExistsRule{},
		GoalGateHasRetryRule{},
		FidelityValidRule{},
	}
}

// ---- start_node ------------------------------------------------------------

// StartNodeRule asserts exactly one start node (shape=Mdiamond or id
// matching `start`/`Start`).
type StartNodeRule struct{}

func (StartNodeRule) Name() string { return "start_node" }

func (StartNodeRule) Apply(g *graph.Graph) []Diagnostic {
	starts := findStartNodes(g)
	switch {
	case len(starts) == 0:
		return []Diagnostic{{
			Rule:     "start_node",
			Severity: Error,
			Message:  "no start node: add a node with shape=Mdiamond or id=start",
		}}
	case len(starts) > 1:
		return []Diagnostic{{
			Rule:     "start_node",
			Severity: Error,
			Message:  fmt.Sprintf("multiple start nodes: %s", strings.Join(starts, ", ")),
		}}
	}
	return nil
}

// ---- terminal_node ---------------------------------------------------------

// TerminalNodeRule asserts exactly one exit node (shape=Msquare or id
// matching `exit`/`end`).
type TerminalNodeRule struct{}

func (TerminalNodeRule) Name() string { return "terminal_node" }

func (TerminalNodeRule) Apply(g *graph.Graph) []Diagnostic {
	exits := findExitNodes(g)
	switch {
	case len(exits) == 0:
		return []Diagnostic{{
			Rule:     "terminal_node",
			Severity: Error,
			Message:  "no exit node: add a node with shape=Msquare or id=exit",
		}}
	case len(exits) > 1:
		return []Diagnostic{{
			Rule:     "terminal_node",
			Severity: Error,
			Message:  fmt.Sprintf("multiple exit nodes: %s", strings.Join(exits, ", ")),
		}}
	}
	return nil
}

// ---- start_no_incoming -----------------------------------------------------

// StartNoIncomingRule asserts the start node has no incoming edges.
type StartNoIncomingRule struct{}

func (StartNoIncomingRule) Name() string { return "start_no_incoming" }

func (StartNoIncomingRule) Apply(g *graph.Graph) []Diagnostic {
	starts := findStartNodes(g)
	if len(starts) != 1 {
		return nil // handled by StartNodeRule
	}
	id := starts[0]
	if len(g.IncomingEdges(id)) > 0 {
		return []Diagnostic{{
			Rule:     "start_no_incoming",
			Severity: Error,
			NodeID:   id,
			Message:  "start node must have no incoming edges",
		}}
	}
	return nil
}

// ---- exit_no_outgoing ------------------------------------------------------

// ExitNoOutgoingRule asserts the exit node has no outgoing edges.
type ExitNoOutgoingRule struct{}

func (ExitNoOutgoingRule) Name() string { return "exit_no_outgoing" }

func (ExitNoOutgoingRule) Apply(g *graph.Graph) []Diagnostic {
	exits := findExitNodes(g)
	if len(exits) != 1 {
		return nil
	}
	id := exits[0]
	if len(g.OutgoingEdges(id)) > 0 {
		return []Diagnostic{{
			Rule:     "exit_no_outgoing",
			Severity: Error,
			NodeID:   id,
			Message:  "exit node must have no outgoing edges",
		}}
	}
	return nil
}

// ---- edge_target_exists ----------------------------------------------------

// EdgeTargetExistsRule reports edges referencing missing node IDs. The
// graph builder synthesises implicit nodes for edge endpoints, so a true
// "missing target" diagnostic only fires when a Graph is hand-built with
// edges referencing IDs never registered as Nodes. Kept here for parity
// with the spec's lint matrix.
type EdgeTargetExistsRule struct{}

func (EdgeTargetExistsRule) Name() string { return "edge_target_exists" }

func (EdgeTargetExistsRule) Apply(g *graph.Graph) []Diagnostic {
	var out []Diagnostic
	for _, e := range g.Edges {
		if _, ok := g.Nodes[e.From]; !ok {
			out = append(out, Diagnostic{
				Rule:     "edge_target_exists",
				Severity: Error,
				EdgeFrom: e.From,
				EdgeTo:   e.To,
				Message:  fmt.Sprintf("edge source %q does not exist", e.From),
			})
		}
		if _, ok := g.Nodes[e.To]; !ok {
			out = append(out, Diagnostic{
				Rule:     "edge_target_exists",
				Severity: Error,
				EdgeFrom: e.From,
				EdgeTo:   e.To,
				Message:  fmt.Sprintf("edge target %q does not exist", e.To),
			})
		}
	}
	return out
}

// ---- reachability ----------------------------------------------------------

// ReachabilityRule checks that every node is reachable from the start
// node via BFS over outgoing edges.
type ReachabilityRule struct{}

func (ReachabilityRule) Name() string { return "reachability" }

func (ReachabilityRule) Apply(g *graph.Graph) []Diagnostic {
	starts := findStartNodes(g)
	if len(starts) != 1 {
		return nil
	}
	visited := map[string]bool{starts[0]: true}
	queue := []string{starts[0]}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, e := range g.OutgoingEdges(id) {
			if !visited[e.To] {
				visited[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}
	var out []Diagnostic
	for _, id := range g.NodeOrder {
		if !visited[id] {
			out = append(out, Diagnostic{
				Rule:     "reachability",
				Severity: Error,
				NodeID:   id,
				Message:  fmt.Sprintf("node %q is unreachable from start", id),
			})
		}
	}
	return out
}

// ---- condition_syntax ------------------------------------------------------

// ConditionSyntaxRule parses each edge condition; parse errors are
// reported as ERROR diagnostics.
type ConditionSyntaxRule struct{}

func (ConditionSyntaxRule) Name() string { return "condition_syntax" }

func (ConditionSyntaxRule) Apply(g *graph.Graph) []Diagnostic {
	var out []Diagnostic
	for _, e := range g.Edges {
		cond := strings.TrimSpace(e.Condition())
		if cond == "" {
			continue
		}
		if _, err := condition.Parse(cond); err != nil {
			out = append(out, Diagnostic{
				Rule:     "condition_syntax",
				Severity: Error,
				EdgeFrom: e.From,
				EdgeTo:   e.To,
				Message:  fmt.Sprintf("invalid condition %q: %v", cond, err),
			})
		}
	}
	return out
}

// ---- prompt_on_llm_nodes ---------------------------------------------------

// PromptOnLLMNodesRule warns when a codergen node has neither a `prompt`
// nor a `label`. Codergen handlers fall back to label when prompt is
// empty, so missing both means a node with no instruction at all.
type PromptOnLLMNodesRule struct{}

func (PromptOnLLMNodesRule) Name() string { return "prompt_on_llm_nodes" }

func (PromptOnLLMNodesRule) Apply(g *graph.Graph) []Diagnostic {
	var out []Diagnostic
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if !strings.HasPrefix(n.Type(), "codergen") {
			continue
		}
		if n.Prompt() == "" && n.Attrs["label"] == "" {
			out = append(out, Diagnostic{
				Rule:     "prompt_on_llm_nodes",
				Severity: Warning,
				NodeID:   id,
				Message:  fmt.Sprintf("codergen node %q has no prompt or label", id),
			})
		}
	}
	return out
}

// ---- type_known ------------------------------------------------------------

// TypeKnownRule warns when a node's explicit `type` attribute is not in
// the recognised set. Custom handler types may be registered at runtime;
// the rule is therefore a WARNING.
type TypeKnownRule struct{}

func (TypeKnownRule) Name() string { return "type_known" }

func (TypeKnownRule) Apply(g *graph.Graph) []Diagnostic {
	known := map[string]struct{}{
		"start": {}, "exit": {}, "codergen": {}, "wait.human": {},
		"conditional": {}, "parallel": {}, "parallel.fan_in": {},
		"tool": {}, "stack.manager_loop": {},
	}
	var out []Diagnostic
	for _, id := range g.NodeOrder {
		t := g.Nodes[id].Attrs["type"]
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "codergen.") {
			continue // handled by CodergenTypeKnownRule
		}
		if _, ok := known[t]; !ok {
			out = append(out, Diagnostic{
				Rule:     "type_known",
				Severity: Warning,
				NodeID:   id,
				Message:  fmt.Sprintf("unknown handler type %q (custom types must be registered)", t),
			})
		}
	}
	return out
}

// ---- acp_command_missing ---------------------------------------------------

// ACPCommandRule warns when a codergen.acp node has no agent command
// from either the node or the graph `acp_command` attribute. Warning
// rather than error because the --acp-cmd flag can still supply one at
// run time.
type ACPCommandRule struct{}

func (ACPCommandRule) Name() string { return "acp_command_missing" }

func (ACPCommandRule) Apply(g *graph.Graph) []Diagnostic {
	if strings.TrimSpace(g.Attrs["acp_command"]) != "" {
		return nil
	}
	var out []Diagnostic
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if n.Attrs["type"] != "codergen.acp" {
			continue
		}
		if strings.TrimSpace(n.Attrs["acp_command"]) != "" {
			continue
		}
		out = append(out, Diagnostic{
			Rule:     "acp_command_missing",
			Severity: Warning,
			NodeID:   id,
			Message:  "codergen.acp node has no acp_command (node or graph attribute); the run will need --acp-cmd",
		})
	}
	return out
}

// ---- codergen_type_known ---------------------------------------------------

// CodergenTypeKnownRule (codergen-backends §2.3) warns when a `codergen.`
// prefix isn't one of the recognised backend variants.
type CodergenTypeKnownRule struct{}

func (CodergenTypeKnownRule) Name() string { return "codergen_type_known" }

func (CodergenTypeKnownRule) Apply(g *graph.Graph) []Diagnostic {
	known := map[string]struct{}{
		"codergen.claude":      {},
		"codergen.claude.tmux": {},
		"codergen.acp":         {},
		"codergen.openai":      {},
		"codergen.gemini":      {},
	}
	var out []Diagnostic
	for _, id := range g.NodeOrder {
		t := g.Nodes[id].Attrs["type"]
		if !strings.HasPrefix(t, "codergen.") {
			continue
		}
		if _, ok := known[t]; !ok {
			out = append(out, Diagnostic{
				Rule:     "codergen_type_known",
				Severity: Warning,
				NodeID:   id,
				Message:  fmt.Sprintf("unknown codergen backend %q", t),
			})
		}
	}
	return out
}

// ---- retry_target_exists --------------------------------------------------

// RetryTargetExistsRule warns when a node references a retry_target or
// fallback_retry_target that doesn't resolve to a real node ID. Same
// check is applied at graph level.
type RetryTargetExistsRule struct{}

func (RetryTargetExistsRule) Name() string { return "retry_target_exists" }

func (RetryTargetExistsRule) Apply(g *graph.Graph) []Diagnostic {
	var out []Diagnostic
	check := func(scope, key, target string) {
		if target == "" {
			return
		}
		if _, ok := g.Nodes[target]; !ok {
			out = append(out, Diagnostic{
				Rule:     "retry_target_exists",
				Severity: Warning,
				NodeID:   scope,
				Message:  fmt.Sprintf("%s=%q references nonexistent node", key, target),
			})
		}
	}
	for _, k := range []string{"retry_target", "fallback_retry_target"} {
		check("graph", k, g.Attrs[k])
	}
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		for _, k := range []string{"retry_target", "fallback_retry_target"} {
			check(id, k, n.Attrs[k])
		}
	}
	return out
}

// ---- goal_gate_has_retry --------------------------------------------------

// GoalGateHasRetryRule warns when a goal-gate node is missing both a
// node-level and graph-level retry target. Without one the engine has
// nowhere to jump if the gate fails.
type GoalGateHasRetryRule struct{}

func (GoalGateHasRetryRule) Name() string { return "goal_gate_has_retry" }

func (GoalGateHasRetryRule) Apply(g *graph.Graph) []Diagnostic {
	var out []Diagnostic
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if !n.Bool("goal_gate") {
			continue
		}
		if n.Attrs["retry_target"] != "" || n.Attrs["fallback_retry_target"] != "" {
			continue
		}
		if g.Attrs["retry_target"] != "" || g.Attrs["fallback_retry_target"] != "" {
			continue
		}
		out = append(out, Diagnostic{
			Rule:     "goal_gate_has_retry",
			Severity: Warning,
			NodeID:   id,
			Message:  fmt.Sprintf("goal_gate node %q has no retry_target / fallback_retry_target", id),
		})
	}
	return out
}

// ---- fidelity_valid ------------------------------------------------------

// FidelityValidRule warns when a fidelity attribute (graph, node, or
// edge) uses a value outside the recognised set.
type FidelityValidRule struct{}

func (FidelityValidRule) Name() string { return "fidelity_valid" }

func (FidelityValidRule) Apply(g *graph.Graph) []Diagnostic {
	var out []Diagnostic
	add := func(rule Diagnostic) { out = append(out, rule) }
	if v := g.Attrs["default_fidelity"]; v != "" {
		if _, ok := validFidelityModes[v]; !ok {
			add(Diagnostic{
				Rule:     "fidelity_valid",
				Severity: Warning,
				Message:  fmt.Sprintf("graph default_fidelity=%q is not a recognised mode", v),
			})
		}
	}
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if v := n.Attrs["fidelity"]; v != "" {
			if _, ok := validFidelityModes[v]; !ok {
				add(Diagnostic{
					Rule:     "fidelity_valid",
					Severity: Warning,
					NodeID:   id,
					Message:  fmt.Sprintf("node %q fidelity=%q unrecognised", id, v),
				})
			}
		}
	}
	for _, e := range g.Edges {
		if v := e.Attrs["fidelity"]; v != "" {
			if _, ok := validFidelityModes[v]; !ok {
				add(Diagnostic{
					Rule:     "fidelity_valid",
					Severity: Warning,
					EdgeFrom: e.From,
					EdgeTo:   e.To,
					Message:  fmt.Sprintf("edge %s->%s fidelity=%q unrecognised", e.From, e.To, v),
				})
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------

func findStartNodes(g *graph.Graph) []string {
	var out []string
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if n.Shape() == "Mdiamond" {
			out = append(out, id)
			continue
		}
		if id == "start" || id == "Start" {
			out = append(out, id)
		}
	}
	return out
}

func findExitNodes(g *graph.Graph) []string {
	var out []string
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if n.Shape() == "Msquare" {
			out = append(out, id)
			continue
		}
		if id == "exit" || id == "end" {
			out = append(out, id)
		}
	}
	return out
}
