package attractor_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The dispatch skill (.claude/skills/dispatching-pipelines/SKILL.md) states a
// per-pipeline -var contract in a markdown table. Those contracts are exactly
// the part that silently rots as pipelines change, so pin them to the real
// pipeline.dot `vars=` declarations. For each documented pipeline: assert its
// pipeline.dot exists, and that the non-check tokens the skill documents equal
// the pipeline's declared vars. The `check.*` tokens are the checks-core
// references (seeded, not declared in vars=), so they are compared separately:
// only the pipelines that embed checks-core may document them.
func TestSkillContractMatchesPipelines(t *testing.T) {
	const skillPath = "../.claude/skills/dispatching-pipelines/SKILL.md"
	src, err := os.ReadFile(skillPath)
	must(t, err)

	// Pipelines that embed checks-core (so `check.*` is part of their real
	// contract) → the skill row must mention it; those that don't → it must not.
	usesChecks := map[string]bool{
		"plan-build-review": true,
		"amend-pr":          true,
		"revise-pr":         true,
		"review-pr":         false,
		"checks":            true,
	}

	documented := parseSkillVarTable(t, string(src))
	for name := range usesChecks {
		row, ok := documented[name]
		if !ok {
			t.Fatalf("skill var table is missing a row for %q", name)
		}

		dotPath := filepath.Join("..", "pipelines", name, "pipeline.dot")
		dot, err := os.ReadFile(dotPath)
		if err != nil {
			t.Fatalf("pipeline %q: %v", name, err)
		}

		declared := parseDeclaredVars(t, name, string(dot))
		if got := setToSorted(row.vars); !equalStrings(got, declared) {
			t.Errorf("skill row %q documents vars %v, but %s declares vars=%v",
				name, got, dotPath, declared)
		}

		if row.hasCheck != usesChecks[name] {
			t.Errorf("skill row %q documents check.* = %v, but pipeline uses checks-core = %v",
				name, row.hasCheck, usesChecks[name])
		}
	}
}

type skillRow struct {
	vars     map[string]bool // non-check tokens
	hasCheck bool            // any check.* token present
}

var tokenRe = regexp.MustCompile("`([^`]+)`")

// parseSkillVarTable reads the markdown table whose header is
// "| Pipeline | `-var` contract |" and folds each row into its backtick
// tokens: check tokens set hasCheck, the rest become the row's var set.
func parseSkillVarTable(t *testing.T, src string) map[string]skillRow {
	t.Helper()
	out := map[string]skillRow{}
	for _, line := range strings.Split(src, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		cols := strings.Split(line, "|")
		if len(cols) < 3 {
			continue
		}
		nameTok := tokenRe.FindStringSubmatch(cols[1])
		if nameTok == nil {
			continue // header / separator / prose row
		}
		name := nameTok[1]
		row := skillRow{vars: map[string]bool{}}
		for _, m := range tokenRe.FindAllStringSubmatch(cols[2], -1) {
			tok := m[1]
			if strings.HasPrefix(tok, "check") {
				row.hasCheck = true
				continue
			}
			row.vars[tok] = true
		}
		out[name] = row
	}
	return out
}

var declaredVarsRe = regexp.MustCompile(`vars\s*=\s*"([^"]*)"`)

// parseDeclaredVars returns the sorted set from a pipeline.dot's `vars="a,b"`.
func parseDeclaredVars(t *testing.T, name, dot string) []string {
	t.Helper()
	m := declaredVarsRe.FindStringSubmatch(dot)
	if m == nil {
		t.Fatalf("pipeline %q: no vars= declaration found", name)
	}
	var out []string
	for _, v := range strings.Split(m[1], ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func setToSorted(s map[string]bool) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
