package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/allouis/attractor/internal/graph"
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
	Mode           FidelityMode
	Goal           string
	RunID          string
	CompletedNodes []string
	NodeOutcomes   map[string]Outcome
	Context        map[string]string
	// Responses maps each completed node ID to the full text the agent
	// wrote to that stage's response.md. Used by compact and summary
	// modes to carry real conversation content rather than the
	// truncated `last_response` value the context stores.
	Responses map[string]string
}

// approxCharsPerToken is a rough conversion for spec §5.4's token
// budgets — close enough for sizing the truncation thresholds applied
// to inlined response content. Real tokenisation lives in the LLM.
const approxCharsPerToken = 4

// BuildPreamble produces the synthesised context carry-over text for
// the next node's LLM session. Compact lists every completed stage as
// a bullet and inlines the latest stage's full response. Summary modes
// list the recent N stages and inline those responses scaled to fit
// §5.4's token budgets. Real LLM-driven summarisation is deferred.
func BuildPreamble(in PreambleInput) string {
	switch in.Mode {
	case FidelityFull:
		return ""
	case FidelityTruncate:
		return fmt.Sprintf("[Goal] %s\n[Run] %s", in.Goal, in.RunID)
	case FidelityCompact:
		return compactPreamble(in)
	case FidelitySummaryLow:
		return summaryPreamble(in, 1, false, 600*approxCharsPerToken)
	case FidelitySummaryMedium:
		return summaryPreamble(in, 3, true, 1500*approxCharsPerToken)
	case FidelitySummaryHigh:
		return summaryPreamble(in, 5, true, 3000*approxCharsPerToken)
	}
	return compactPreamble(in)
}

// compactPreamble follows §5.4's compact spec: a bullet list of every
// completed stage with its outcome, the active context, and an inlined
// copy of the most recent stage's response so the next agent sees the
// real prior turn rather than a truncated summary.
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
	// Inline the most recent stage's response in full.
	if n := len(in.CompletedNodes); n > 0 && in.Responses != nil {
		last := in.CompletedNodes[n-1]
		if body := strings.TrimSpace(in.Responses[last]); body != "" {
			b.WriteString(fmt.Sprintf("\n---\n[%s response.md]\n%s\n", last, body))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// summaryPreamble renders the recent N stages plus their inlined
// responses, budget-truncated across the included set.
func summaryPreamble(in PreambleInput, recentLimit int, includeContext bool, budgetChars int) string {
	var b strings.Builder
	if in.Goal != "" {
		b.WriteString("Goal: " + in.Goal + "\n")
	}
	completed := in.CompletedNodes
	if len(completed) > recentLimit {
		completed = completed[len(completed)-recentLimit:]
	}
	if len(completed) > 0 {
		b.WriteString(fmt.Sprintf("Recent %d stage(s):\n", len(completed)))
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
	if len(completed) > 0 && len(in.Responses) > 0 {
		perStage := 0
		if budgetChars > 0 && len(completed) > 0 {
			perStage = budgetChars / len(completed)
		}
		for _, id := range completed {
			body, ok := in.Responses[id]
			if !ok || strings.TrimSpace(body) == "" {
				continue
			}
			if perStage > 0 && len(body) > perStage {
				body = body[:perStage] + "\n…[truncated]"
			}
			b.WriteString(fmt.Sprintf("\n---\n[%s response.md]\n%s\n", id, strings.TrimRight(body, "\n")))
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
