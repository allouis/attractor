package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fabro/attractor/internal/graph"
)

// FidelityMode controls how much prior conversation/context is carried
// into a node's LLM session (spec §5.4).
type FidelityMode string

const (
	FidelityFull          FidelityMode = "full"
	FidelityTruncate      FidelityMode = "truncate"
	FidelityCompact       FidelityMode = "compact"
	FidelitySummaryLow    FidelityMode = "summary:low"
	FidelitySummaryMedium FidelityMode = "summary:medium"
	FidelitySummaryHigh   FidelityMode = "summary:high"
)

// IsValid reports whether v names a recognised fidelity mode.
func IsValidFidelity(v string) bool {
	switch FidelityMode(v) {
	case FidelityFull, FidelityTruncate, FidelityCompact,
		FidelitySummaryLow, FidelitySummaryMedium, FidelitySummaryHigh:
		return true
	}
	return false
}

// ResolveFidelity returns the resolved fidelity for a target node.
// Precedence (§5.4): edge → target node → graph default → built-in
// fallback "compact". The incoming edge may be nil for the start node.
func ResolveFidelity(edge *graph.Edge, target *graph.Node, g *graph.Graph) FidelityMode {
	if edge != nil {
		if v := edge.Attr("fidelity"); v != "" {
			return FidelityMode(v)
		}
	}
	if v := target.Attrs["fidelity"]; v != "" {
		return FidelityMode(v)
	}
	if v := g.Attrs["default_fidelity"]; v != "" {
		return FidelityMode(v)
	}
	return FidelityCompact
}

// ResolveThread picks the conversation thread key for a node under
// `full` fidelity (§5.4). The fallback ladder: target node thread_id →
// edge thread_id → graph default → previous node id.
func ResolveThread(edge *graph.Edge, target *graph.Node, g *graph.Graph, previousNodeID string) string {
	if v := target.Attrs["thread_id"]; v != "" {
		return v
	}
	if edge != nil {
		if v := edge.Attr("thread_id"); v != "" {
			return v
		}
	}
	if v := g.Attrs["default_thread"]; v != "" {
		return v
	}
	if previousNodeID != "" {
		return previousNodeID
	}
	return target.ID
}

// PreambleInput is the materialised state the preamble synthesiser
// reads when constructing the carry-over text for the next node.
type PreambleInput struct {
	Mode         FidelityMode
	Goal         string
	RunID        string
	CompletedNodes []string
	NodeOutcomes  map[string]Outcome
	Context       map[string]string
}

// BuildPreamble produces the synthesised context carry-over text for
// the next node's LLM session. Real summarisation modes are
// placeholders for an LLM-driven synthesis pass in a follow-up; for
// now the modes emit deterministic, scaled bullet renderings.
func BuildPreamble(in PreambleInput) string {
	switch in.Mode {
	case FidelityFull:
		return ""
	case FidelityTruncate:
		return fmt.Sprintf("[Goal] %s\n[Run] %s", in.Goal, in.RunID)
	case FidelityCompact:
		return compactPreamble(in)
	case FidelitySummaryLow:
		return summaryPreamble(in, 1, false)
	case FidelitySummaryMedium:
		return summaryPreamble(in, 3, true)
	case FidelitySummaryHigh:
		return summaryPreamble(in, 5, true)
	}
	return compactPreamble(in)
}

func compactPreamble(in PreambleInput) string {
	var b strings.Builder
	if in.Goal != "" {
		b.WriteString("Goal: " + in.Goal + "\n")
	}
	if len(in.CompletedNodes) > 0 {
		b.WriteString("Completed stages:\n")
		for _, id := range in.CompletedNodes {
			oc := in.NodeOutcomes[id]
			b.WriteString(fmt.Sprintf("  - %s [%s]", id, oc.Status))
			if oc.Notes != "" {
				b.WriteString(": " + oc.Notes)
			}
			b.WriteString("\n")
		}
	}
	if len(in.Context) > 0 {
		ctxKeys := relevantContextKeys(in.Context)
		if len(ctxKeys) > 0 {
			b.WriteString("Context:\n")
			for _, k := range ctxKeys {
				b.WriteString(fmt.Sprintf("  - %s = %s\n", k, in.Context[k]))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func summaryPreamble(in PreambleInput, recentLimit int, includeContext bool) string {
	var b strings.Builder
	if in.Goal != "" {
		b.WriteString("Goal: " + in.Goal + "\n")
	}
	completed := in.CompletedNodes
	if len(completed) > recentLimit {
		completed = completed[len(completed)-recentLimit:]
	}
	if len(completed) > 0 {
		b.WriteString(fmt.Sprintf("Recent %d stages:\n", len(completed)))
		for _, id := range completed {
			oc := in.NodeOutcomes[id]
			b.WriteString(fmt.Sprintf("  - %s [%s]\n", id, oc.Status))
		}
	}
	if includeContext {
		keys := relevantContextKeys(in.Context)
		if len(keys) > 0 {
			b.WriteString("Active context:\n")
			for _, k := range keys {
				b.WriteString(fmt.Sprintf("  - %s = %s\n", k, in.Context[k]))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// relevantContextKeys filters the run context for keys frontends care
// about: drops internal / engine bookkeeping, returns sorted output.
func relevantContextKeys(ctx map[string]string) []string {
	var keys []string
	for k := range ctx {
		if strings.HasPrefix(k, "internal.") {
			continue
		}
		if k == "outcome" || k == "preferred_label" || k == "current_node" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
