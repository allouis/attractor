package engine

import (
	"sort"
	"strings"

	"github.com/allouis/attractor/internal/condition"
	"github.com/allouis/attractor/internal/graph"
)

// SelectEdge implements the 5-step edge selection algorithm from spec
// §3.3. It returns nil if no edge is eligible.
func SelectEdge(node *graph.Node, outcome *Outcome, ctx *Context, g *graph.Graph) *graph.Edge {
	edges := g.OutgoingEdges(node.ID)
	if len(edges) == 0 {
		return nil
	}
	resolver := Resolver{Outcome: outcome, Context: ctx}

	// Step 1: condition-matching edges win first.
	var conditionMatched []*graph.Edge
	for _, e := range edges {
		cond := strings.TrimSpace(e.Condition())
		if cond == "" {
			continue
		}
		expr, err := condition.Parse(cond)
		if err != nil {
			continue
		}
		if expr.Evaluate(resolver) {
			conditionMatched = append(conditionMatched, e)
		}
	}
	if len(conditionMatched) > 0 {
		return bestByWeightThenLexical(conditionMatched)
	}

	// On FAIL, only condition-matching edges (notably `outcome=fail`)
	// are eligible. Steps 2–5 below assume success-path routing and
	// must not silently swallow a failure. The engine's
	// advanceOrFail handler picks up from here with retry_target /
	// fallback_retry_target lookups (spec §3.7).
	if outcome != nil && outcome.Status == StatusFail {
		return nil
	}

	// Step 2: preferred-label fall-through against unconditional edges.
	if outcome != nil && outcome.PreferredLabel != "" {
		want := normalizeLabel(outcome.PreferredLabel)
		for _, e := range edges {
			if e.Condition() != "" {
				continue
			}
			if normalizeLabel(e.Label()) == want {
				return e
			}
		}
	}

	// Step 3: suggested next IDs in declaration order against
	// unconditional edges.
	if outcome != nil {
		for _, id := range outcome.SuggestedNextIDs {
			for _, e := range edges {
				if e.Condition() != "" {
					continue
				}
				if e.To == id {
					return e
				}
			}
		}
	}

	// Step 4 & 5: highest weight, lexical tiebreak.
	var unconditional []*graph.Edge
	for _, e := range edges {
		if e.Condition() == "" {
			unconditional = append(unconditional, e)
		}
	}
	if len(unconditional) > 0 {
		return bestByWeightThenLexical(unconditional)
	}
	return nil
}

func bestByWeightThenLexical(edges []*graph.Edge) *graph.Edge {
	cp := append([]*graph.Edge{}, edges...)
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].Weight() != cp[j].Weight() {
			return cp[i].Weight() > cp[j].Weight()
		}
		return cp[i].To < cp[j].To
	})
	return cp[0]
}

// normalizeLabel applies the label-matching normalization from spec
// §3.3: lowercase, trim whitespace, strip accelerator prefixes.
func normalizeLabel(label string) string {
	label = strings.TrimSpace(label)
	label = stripAccelerator(label)
	return strings.ToLower(strings.TrimSpace(label))
}

func stripAccelerator(label string) string {
	// `[K] text`
	if len(label) > 3 && label[0] == '[' {
		if idx := strings.Index(label, "]"); idx >= 0 {
			return strings.TrimSpace(label[idx+1:])
		}
	}
	// `K) text` or `K - text` — single-char prefix followed by `)` / `-` / `.`.
	if len(label) > 2 {
		c := label[0]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') {
			sep := label[1]
			if sep == ')' || sep == '-' || sep == '.' {
				return strings.TrimSpace(label[2:])
			}
			if sep == ' ' && len(label) > 3 && (label[2] == '-' || label[2] == '.') {
				return strings.TrimSpace(label[3:])
			}
		}
	}
	return label
}
