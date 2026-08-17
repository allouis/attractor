package attractor_test

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The dispatch skill (.claude/skills/dispatching-pipelines/SKILL.md) states a
// per-pipeline -var contract in a markdown table. Those contracts are exactly
// the part that silently rots as pipelines change, so pin them to the real
// pipeline.dot on disk. For each pipeline the skill documents: assert its
// pipeline.dot exists, that the non-check tokens the skill lists equal the
// pipeline's declared `vars=`, and that the skill mentions `check.*` iff the
// pipeline actually embeds the checks-core subgraph. Both sides are derived
// from disk — nothing about the contract is hardcoded here, so the guard
// cannot drift in lockstep with the thing it guards.
func TestSkillContractMatchesPipelines(t *testing.T) {
	const skillPath = "../.claude/skills/dispatching-pipelines/SKILL.md"
	src, err := os.ReadFile(skillPath)
	must(t, err)

	// The pipelines the skill's table is expected to cover. Their contracts
	// are read from disk below; this list only pins that no row goes missing.
	want := []string{"plan-build-review", "amend-pr", "revise-pr", "review-pr", "checks"}

	documented := parseSkillVarTable(t, string(src))
	for _, name := range want {
		row, ok := documented[name]
		if !ok {
			t.Errorf("skill var table is missing a row for %q", name)
			continue
		}

		dotPath := filepath.Join("..", "pipelines", name, "pipeline.dot")
		dot, err := os.ReadFile(dotPath)
		if err != nil {
			t.Errorf("pipeline %q: %v", name, err)
			continue
		}

		if declared := parseDeclaredVars(t, name, string(dot)); !slices.Equal(row.vars, declared) {
			t.Errorf("skill row %q lists vars %v, but %s declares vars=%v",
				name, row.vars, dotPath, declared)
		}

		embedsChecks := strings.Contains(string(dot), "checks-core/pipeline.dot")
		if row.hasCheck != embedsChecks {
			t.Errorf("skill row %q documents check.* = %v, but %s embeds checks-core = %v",
				name, row.hasCheck, dotPath, embedsChecks)
		}
	}
}

type skillRow struct {
	vars     []string // sorted non-check tokens
	hasCheck bool     // any check.* token present
}

var tokenRe = regexp.MustCompile("`([^`]+)`")

// parseSkillVarTable reads the markdown table introduced by the
// "| Pipeline | `-var` contract |" header and folds each following row into
// its backtick tokens: a `check.` token sets hasCheck, the rest become the
// row's sorted var set. Anchoring to the header keeps stray backtick-bearing
// tables elsewhere in the doc from being mistaken for the contract.
func parseSkillVarTable(t *testing.T, src string) map[string]skillRow {
	t.Helper()
	out := map[string]skillRow{}
	inTable := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inTable {
			if strings.HasPrefix(trimmed, "|") && strings.Contains(line, "Pipeline") && strings.Contains(line, "-var") {
				inTable = true
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			break // table ended
		}
		cols := strings.Split(line, "|")
		if len(cols) < 3 {
			continue
		}
		nameTok := tokenRe.FindStringSubmatch(cols[1])
		if nameTok == nil {
			continue // the |---|---| separator row
		}
		set := map[string]bool{}
		row := skillRow{}
		for _, m := range tokenRe.FindAllStringSubmatch(cols[2], -1) {
			if strings.HasPrefix(m[1], "check.") {
				row.hasCheck = true
				continue
			}
			set[m[1]] = true
		}
		row.vars = slices.Sorted(maps.Keys(set))
		out[nameTok[1]] = row
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
	slices.Sort(out)
	return out
}
