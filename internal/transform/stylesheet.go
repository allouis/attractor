package transform

import (
	"fmt"
	"sort"
	"strings"

	"github.com/allouis/attractor/internal/graph"
)

// Stylesheet applies CSS-like rules from an EXTERNAL stylesheet
// (`attractor run --stylesheet <file>`), not the graph itself — pipelines
// carry structure and class tags, models live in the sheet. Selectors
// match by shape, class, or node ID; specificity orders the resolution.
// Applied after subgraph inlining, so a rule can target inlined nodes by
// their prefixed ID (e.g. `#review_loop.synth`) or their class.
type Stylesheet struct {
	// Source is the raw stylesheet text (the concatenation of the
	// --stylesheet files). Empty means no overlay.
	Source string
}

// Apply parses the stylesheet source, then walks each node and overlays
// property values for `llm_model`, `llm_provider`, and
// `reasoning_effort`. Explicit node attributes (already present) take
// precedence and are never overwritten, so a mandatory pin (e.g. the
// codex review lens) survives the sheet.
func (s Stylesheet) Apply(g *graph.Graph) (*graph.Graph, error) {
	src := s.Source
	if strings.TrimSpace(src) == "" {
		return g, nil
	}
	rules, err := parseStylesheet(src)
	if err != nil {
		return nil, fmt.Errorf("model_stylesheet: %w", err)
	}
	allowedProps := map[string]struct{}{
		"llm_model":        {},
		"llm_provider":     {},
		"reasoning_effort": {},
	}
	for _, id := range g.NodeOrder {
		node := g.Nodes[id]
		// Snapshot explicit attrs so the stylesheet never overwrites them.
		explicit := map[string]struct{}{}
		for k, v := range node.Attrs {
			if _, ok := allowedProps[k]; !ok {
				continue
			}
			if v != "" {
				explicit[k] = struct{}{}
			}
		}
		var matched []stylesheetRule
		for _, r := range rules {
			if r.matches(node) {
				matched = append(matched, r)
			}
		}
		// Ascending specificity: later writes win, but explicit attrs
		// remain authoritative regardless of source order.
		sort.SliceStable(matched, func(i, j int) bool {
			return matched[i].specificity < matched[j].specificity
		})
		for _, r := range matched {
			for k, v := range r.props {
				if _, allowed := allowedProps[k]; !allowed {
					continue
				}
				if _, isExplicit := explicit[k]; isExplicit {
					continue
				}
				node.Attrs[k] = v
			}
		}
	}
	return g, nil
}

type stylesheetRule struct {
	kind        selectorKind
	value       string
	props       map[string]string
	specificity int
}

type selectorKind int

const (
	selUniversal selectorKind = iota
	selShape
	selClass
	selID
)

func (r stylesheetRule) matches(n *graph.Node) bool {
	switch r.kind {
	case selUniversal:
		return true
	case selShape:
		return n.Shape() == r.value
	case selClass:
		for _, c := range n.Classes() {
			if c == r.value {
				return true
			}
		}
		return false
	case selID:
		return n.ID == r.value
	}
	return false
}

// parseStylesheet implements a small CSS-like parser. Tolerant of
// extra whitespace and trailing semicolons; rejects invalid selectors.
func parseStylesheet(src string) ([]stylesheetRule, error) {
	src = stripComments(src)
	var rules []stylesheetRule
	i := 0
	for i < len(src) {
		i = skipWS(src, i)
		if i >= len(src) {
			break
		}
		selStart := i
		for i < len(src) && src[i] != '{' {
			i++
		}
		if i >= len(src) {
			return nil, fmt.Errorf("missing `{` after selector %q", strings.TrimSpace(src[selStart:]))
		}
		selector := strings.TrimSpace(src[selStart:i])
		i++ // consume {
		bodyStart := i
		for i < len(src) && src[i] != '}' {
			i++
		}
		if i >= len(src) {
			return nil, fmt.Errorf("missing `}` for selector %q", selector)
		}
		body := src[bodyStart:i]
		i++ // consume }
		rule, err := buildRule(selector, body)
		if err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func buildRule(selector, body string) (stylesheetRule, error) {
	rule := stylesheetRule{props: map[string]string{}}
	switch {
	case selector == "*":
		rule.kind = selUniversal
		rule.specificity = 0
	case strings.HasPrefix(selector, "#"):
		rule.kind = selID
		rule.value = selector[1:]
		rule.specificity = 3
	case strings.HasPrefix(selector, "."):
		rule.kind = selClass
		rule.value = selector[1:]
		rule.specificity = 2
	default:
		rule.kind = selShape
		rule.value = selector
		rule.specificity = 1
	}
	for _, decl := range strings.Split(body, ";") {
		decl = strings.TrimSpace(decl)
		if decl == "" {
			continue
		}
		parts := strings.SplitN(decl, ":", 2)
		if len(parts) != 2 {
			return rule, fmt.Errorf("invalid declaration %q", decl)
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, `"`)
		rule.props[key] = val
	}
	return rule, nil
}

// stripComments removes C-style /* ... */ comments so a stylesheet can
// carry documentation. Unterminated comments run to end-of-input.
func stripComments(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		if i+1 < len(src) && src[i] == '/' && src[i+1] == '*' {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 2
			continue
		}
		b.WriteByte(src[i])
		i++
	}
	return b.String()
}

func skipWS(src string, i int) int {
	for i < len(src) {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		break
	}
	return i
}
