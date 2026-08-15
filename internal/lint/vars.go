package lint

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/allouis/attractor/internal/graph"
)

// ---- context_refs ----------------------------------------------------------

// ContextRefsRule cross-checks $context.* references against the keys
// the run can actually hold: declared vars=, node outputs (output_key,
// plus the documentation attr outputs= for keys an agent sets via its
// status.json context_updates), and well-known runtime keys. Undeclared
// references warn (a typo'd key would otherwise fail interpolation
// mid-run); declared-but-unused vars warn too. Warnings never block —
// context is dynamic and a seeded key the graph doesn't mention is
// legal (local-first plan P1.3).
type ContextRefsRule struct{}

// Name implements Rule.
func (ContextRefsRule) Name() string { return "context_refs" }

// dollarRef matches $context.some.key references in any attr value.
var dollarRef = regexp.MustCompile(`\$context\.([a-zA-Z0-9_]+(?:\.[a-zA-Z0-9_]+)*)`)

// bareRef matches bare context.some.key references inside condition
// expressions (the condition syntax has no $ sigil).
var bareRef = regexp.MustCompile(`(?:^|[^$.\w])context\.([a-zA-Z0-9_]+(?:\.[a-zA-Z0-9_]+)*)`)

// runtimePrefixes are namespaces owned by the engine, handlers, or the
// hosting process at runtime; references into them are always legal.
var runtimePrefixes = []string{
	"graph.",    // graph attrs mirrored at run start
	"human.",    // wait.human gate answers
	"check.",    // repo check commands seeded from config
	"tool.",     // tool handler output/exit_code/stderr
	"parallel.", // parallel handler fan-in results
	"internal.", // engine bookkeeping
}

// runtimeKeys are individual keys the engine or the codergen handler
// maintains on every run, plus declared inputs consumed by whatever
// launches the run rather than by graph text (workspace_revision: an
// external launcher materializes the workspace at that revision, so
// revise-pr declares it without referencing it — local-first plan D9).
var runtimeKeys = map[string]struct{}{
	"outcome":            {},
	"preferred_label":    {},
	"failure_reason":     {},
	"last_stage":         {},
	"last_response":      {},
	"current_node":       {},
	"workspace_revision": {},
}

// conditionAttr reports whether an attribute holds a condition
// expression (bare context.* syntax) rather than free text.
func conditionAttr(key string) bool {
	return key == "condition" || strings.HasSuffix(key, "stop_condition")
}

// Apply implements Rule.
func (ContextRefsRule) Apply(g *graph.Graph) []Diagnostic {
	declared := map[string]struct{}{}
	for _, v := range g.DeclaredVars() {
		declared[v] = struct{}{}
	}
	produced := map[string]struct{}{}
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if k := n.OutputKey(); k != "" {
			produced[k] = struct{}{}
		}
		for _, k := range strings.Split(n.Attrs["outputs"], ",") {
			if k = strings.TrimSpace(k); k != "" {
				produced[k] = struct{}{}
			}
		}
	}

	// refs maps each referenced key to one location it appears at (the
	// first seen, for the diagnostic).
	refs := map[string]string{}
	collect := func(where string, attrs map[string]string) {
		for key, val := range attrs {
			for _, m := range dollarRef.FindAllStringSubmatch(val, -1) {
				if _, seen := refs[m[1]]; !seen {
					refs[m[1]] = where
				}
			}
			if conditionAttr(key) {
				for _, m := range bareRef.FindAllStringSubmatch(val, -1) {
					if _, seen := refs[m[1]]; !seen {
						refs[m[1]] = where
					}
				}
			}
		}
	}
	collect("graph", g.Attrs)
	for _, id := range g.NodeOrder {
		collect("node "+id, g.Nodes[id].Attrs)
	}
	for _, e := range g.Edges {
		collect(fmt.Sprintf("edge %s -> %s", e.From, e.To), e.Attrs)
	}

	var out []Diagnostic
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if knownKey(name, declared, produced) {
			continue
		}
		out = append(out, Diagnostic{
			Rule:     "context_refs",
			Severity: Warning,
			Message: fmt.Sprintf("%s references $context.%s, which is not a declared var, node output, or runtime key — typo?",
				refs[name], name),
			Fix: fmt.Sprintf("add %q to the graph vars= list, or outputs=%q on the node that produces it", name, name),
		})
	}

	unused := make([]string, 0)
	for name := range declared {
		if _, ok := refs[name]; ok {
			continue
		}
		// Machinery-consumed inputs (see runtimeKeys) are used without
		// ever appearing in graph text.
		if _, ok := runtimeKeys[name]; ok {
			continue
		}
		unused = append(unused, name)
	}
	sort.Strings(unused)
	for _, name := range unused {
		out = append(out, Diagnostic{
			Rule:     "context_refs",
			Severity: Warning,
			Message:  fmt.Sprintf("var %q is declared in vars= but never referenced", name),
			Fix:      "remove it from vars=, or reference it as $context." + name,
		})
	}
	return out
}

// knownKey reports whether a referenced key resolves against the
// declared vars, produced outputs, or runtime namespaces. A reference
// to a nested path under a declared/produced key ("repo.name" when
// "repo" is declared) does not count — context keys are flat strings.
func knownKey(name string, declared, produced map[string]struct{}) bool {
	if _, ok := declared[name]; ok {
		return true
	}
	if _, ok := produced[name]; ok {
		return true
	}
	if _, ok := runtimeKeys[name]; ok {
		return true
	}
	for _, p := range runtimePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
