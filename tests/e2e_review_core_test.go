package attractor_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/backend/fake"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/lint"
)

// reviewCoreSrc reads the shared multi-lens review sub-pipeline
// (review-pipeline-spec RV2).
func reviewCoreSrc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../pipelines/review-core/pipeline.dot")
	must(t, err)
	return string(b)
}

// reviewCoreLenses are the five lens node IDs the sub-pipeline fans out to.
var reviewCoreLenses = []string{
	"correctness", "design", "prod_safety", "simplification", "tests",
}

// TestReviewCore_LintsClean confirms the shared review-core pipeline is
// structurally valid: it parses, builds, and lints with no ERROR (RV2).
func TestReviewCore_LintsClean(t *testing.T) {
	g := build(t, reviewCoreSrc(t))
	diags, err := lint.ValidateOrError(g)
	if err != nil {
		t.Fatalf("review-core pipeline rejected: %v", err)
	}
	for _, d := range diags {
		if d.Severity == lint.Error {
			t.Fatalf("unexpected ERROR: %+v", d)
		}
	}
}

// TestReviewCore_FanOutToSynth pins the RV2 contract: the parallel fan-out
// runs all five lenses concurrently, each lens's full finding rides context
// into parallel.results, and synth reads every finding via
// $context.parallel.results — proven by the findings appearing in synth's
// interpolated prompt.md. synth self-reports the verdict, which becomes the
// sub-pipeline's terminal outcome.
func TestReviewCore_FanOutToSynth(t *testing.T) {
	be := fake.New()
	markers := map[string]string{}
	for _, lens := range reviewCoreLenses {
		// Marker is plain ASCII so it survives JSON encoding into
		// parallel.results and the prompt interpolation intact.
		marker := "LENSFINDING" + strings.ToUpper(strings.ReplaceAll(lens, "_", ""))
		markers[lens] = marker
		be.SetText(lens, marker)
	}
	be.SetSequence("synth", fake.Step{Outcome: &engine.Outcome{
		Status: engine.StatusSuccess,
		Notes:  "merged review",
		ContextUpdates: map[string]string{
			"review.summary": "all lenses clear",
			"review.verdict": "pass",
		},
	}})

	out, _, logs := runFixtureSeeded(t, reviewCoreSrc(t), be, nil,
		map[string]string{"diff_cmd": "jj diff"})

	// Every lens ran exactly once, concurrently under the fan-out.
	for _, lens := range reviewCoreLenses {
		if n := be.CallCount(lens); n != 1 {
			t.Fatalf("lens %q called %d times, want 1", lens, n)
		}
	}

	// The fan-out bundled all five branch outcomes into parallel.results.
	fanOut := readStatus(t, logs, "fan_out")
	raw := fanOut.ContextUpdates["parallel.results"]
	if raw == "" {
		t.Fatal("parallel.results missing from fan_out outcome")
	}
	var results []struct {
		Branch  string         `json:"branch"`
		Outcome engine.Outcome `json:"outcome"`
	}
	must(t, json.Unmarshal([]byte(raw), &results))
	if len(results) != len(reviewCoreLenses) {
		t.Fatalf("parallel.results has %d branches, want %d", len(results), len(reviewCoreLenses))
	}

	// synth read every lens finding via $context.parallel.results: each
	// marker is present in synth's interpolated prompt.
	promptMD, err := os.ReadFile(filepath.Join(logs, "synth", "prompt.md"))
	must(t, err)
	for lens, marker := range markers {
		if !strings.Contains(string(promptMD), marker) {
			t.Fatalf("synth prompt.md missing %q finding %q", lens, marker)
		}
	}

	// review-core's terminal outcome is synth's self-reported verdict.
	if out.Status != engine.StatusSuccess {
		t.Fatalf("terminal status = %v, want SUCCESS", out.Status)
	}
	synth := readStatus(t, logs, "synth")
	if got := synth.ContextUpdates["review.verdict"]; got != "pass" {
		t.Fatalf("synth verdict = %q, want pass", got)
	}
}

// TestReviewCore_SynthFailIsTerminal guards the branch RV3/RV4 route on: a
// blocking synth verdict surfaces as a FAIL terminal outcome.
func TestReviewCore_SynthFailIsTerminal(t *testing.T) {
	be := fake.New()
	for _, lens := range reviewCoreLenses {
		be.SetText(lens, "finding")
	}
	be.SetSequence("synth", fake.Step{Outcome: &engine.Outcome{
		Status:        engine.StatusFail,
		FailureReason: "correctness lens found a blocking bug",
		ContextUpdates: map[string]string{
			"review.verdict": "fail",
		},
	}})

	// A FAIL outcome is not SUCCESS-class, so the engine does not persist a
	// synth/status.json; synth's FAIL outcome surfaces as the terminal one.
	out, _, _ := runFixtureSeeded(t, reviewCoreSrc(t), be, nil,
		map[string]string{"diff_cmd": "jj diff"})

	if out.Status != engine.StatusFail {
		t.Fatalf("terminal status = %v, want FAIL", out.Status)
	}
	if got := out.ContextUpdates["review.verdict"]; got != "fail" {
		t.Fatalf("terminal verdict = %q, want fail", got)
	}
}
