package server

import (
	"strings"
	"testing"
)

// TestRunNeedsHumanGating: the fleet's needs-human flag is gated on the run
// still being live (web-ui-v2-spec U5 — needs-human made loud in the fleet).
// A blocked-but-live run flags; a terminal run (even one whose question was
// never formally answered, e.g. cancelled mid-wait) stops nagging.
func TestRunNeedsHumanGating(t *testing.T) {
	if got := evalUI(t, `runNeedsHuman({needs_human:true, status:'running'})`); got != "true" {
		t.Errorf("live blocked run should need a human, got %q", got)
	}
	if got := evalUI(t, `runNeedsHuman({needs_human:true, status:'failed'})`); got != "false" {
		t.Errorf("terminal run must not need a human, got %q", got)
	}
	if got := evalUI(t, `runNeedsHuman({needs_human:true, status:'cancelled'})`); got != "false" {
		t.Errorf("cancelled run must not need a human, got %q", got)
	}
	if got := evalUI(t, `runNeedsHuman({status:'running'})`); got != "false" {
		t.Errorf("unblocked run should not need a human, got %q", got)
	}
}

// TestRunRowCarriesNeedsHumanChip: a live blocked run's fleet row carries the
// loud needs-human chip; a terminal run's row does not.
func TestRunRowCarriesNeedsHumanChip(t *testing.T) {
	blocked := evalUI(t, `runRowHtml({id:'abc', status:'running', needs_human:true})`)
	if !strings.Contains(blocked, "chip needs-human") {
		t.Errorf("blocked run row missing needs-human chip:\n%s", blocked)
	}
	terminal := evalUI(t, `runRowHtml({id:'abc', status:'failed', needs_human:true})`)
	if strings.Contains(terminal, "chip needs-human") {
		t.Errorf("terminal run row must not carry needs-human chip:\n%s", terminal)
	}
}

// TestNeedsHumanCountPillLoud: a non-zero needs-human count fills (loud)
// rather than outlines — the fleet's highest-attention treatment; zero does
// not, and other states never go loud.
func TestNeedsHumanCountPillLoud(t *testing.T) {
	if got := evalUI(t, `countPill('needs-human', 2)`); !strings.Contains(got, "loud") {
		t.Errorf("non-zero needs-human pill should be loud:\n%s", got)
	}
	if got := evalUI(t, `countPill('needs-human', 0)`); strings.Contains(got, "loud") {
		t.Errorf("zero needs-human pill must not be loud:\n%s", got)
	}
	if got := evalUI(t, `countPill('running', 5)`); strings.Contains(got, "loud") {
		t.Errorf("running pill must never go loud:\n%s", got)
	}
}
