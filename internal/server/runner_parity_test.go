package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ui-run-view-v3 P4: the runner-parity net. The `direct` launcher runs the
// engine in-process; the `local` launcher reproduces the VM's data path — a
// child `attractor run --report-to` subprocess that phones its events and
// artifacts home, with no daemon-side engine — without needing KVM. This
// suite runs the SAME pipeline under both and asserts the SAME daemon API
// surface (/state, /nodes, /stages incl. a tool stage's stdout, /diff,
// /questions), so a runner whose surface silently regresses is caught here
// rather than in a live VM run. It is the regression net the spec says was
// missing (root cause: "VM/subprocess runs have a poorer API surface … and
// no test asserts parity").
//
// The pipeline is deterministic under the child's default simulation backend:
// tool nodes with known stdout, a codergen node (simulation prose), a
// wait.human gate answered over HTTP, and a deliberate tool failure whose
// fail edge re-enters the node (attempts=2) for the fail-aware assertions.
const parityPipeline = `digraph parity {
  graph [goal="runner parity e2e"]

  start  [shape=Mdiamond]
  greet  [type="tool", tool_command="echo greetings-42"]
  gate   [shape=hexagon, label="Approve the change?"]
  review [type="codergen", prompt="assess the produced change"]
  flaky  [type="tool", tool_command="if [ -e flaky.done ]; then echo flaky-recovered; else echo flaky-first-attempt; touch flaky.done; exit 1; fi"]
  write  [type="tool", tool_command="echo produced-by-parity-run > result.txt; echo done-writing"]
  done   [shape=Msquare]

  start  -> greet
  greet  -> gate
  gate   -> review [label="[A] Approve"]
  review -> flaky
  flaky  -> flaky  [condition="outcome=fail"]
  flaky  -> write  [condition="outcome=success"]
  write  -> done
}`

// parityRunner is one row of the launcher matrix. The suite body is shared:
// a runner is added here (a future `vm` row) by naming it, wiring its
// launcher into the daemon Config, and declaring its ONE intentional
// asymmetry — liveDiffStatus, the /diff verdict WHILE the run is gated (not
// yet terminal). Everything else must match across runners; a divergence
// surfaces as a named subtest failure ("<runner>/<endpoint>"), not a silent
// gap. direct/local cannot compute a live diff (the tip is only stamped at
// terminal), so their gated /diff is not-computable; a vm run would resolve
// its tip live from the shared-store workspace and read "ok"/"no-commits-yet"
// instead — that difference belongs in this field, asserted, never ignored.
type parityRunner struct {
	name           string
	liveDiffStatus diffStatus
	config         func(t *testing.T, logsRoot string) Config
}

func TestRunnerParity(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e: builds the attractor binary and drives jj")
	}
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH; the diff-parity assertions need a jj repo")
	}

	// Build the local runner's child binary BEFORE going hermetic, so `go
	// build` still sees the real module/build cache under the real HOME.
	bin := buildAttractor(t)

	// A hermetic HOME with no provider config: the local child's codergen
	// nodes then take the router's simulation fallback (it is launched
	// without an explicit --backend), so the pipeline is deterministic and
	// touches no real provider/network. The direct runner already simulates
	// (its daemon wires a nil codergen backend), and jj's read/describe paths
	// tolerate the empty identity a fresh HOME leaves.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".data"))

	runners := []parityRunner{
		{
			name:           "direct",
			liveDiffStatus: diffStatusNotComputable,
			config: func(t *testing.T, logsRoot string) Config {
				return Config{Addr: "127.0.0.1:0", LogsRoot: logsRoot, Launcher: directLauncher{}}
			},
		},
		{
			name:           "local",
			liveDiffStatus: diffStatusNotComputable,
			config: func(t *testing.T, logsRoot string) Config {
				l := localLauncher{bin: bin}
				// Register under "local" AND make it the default so the run's
				// placement reads "local" (state parity) while every dispatch,
				// whatever its resolved placement, lands in the local launcher.
				return Config{
					Addr: "127.0.0.1:0", LogsRoot: logsRoot,
					Launcher: l, DefaultRunner: "local",
					Launchers: map[string]Launcher{"local": l},
				}
			},
		},
	}

	for _, rn := range runners {
		t.Run(rn.name, func(t *testing.T) {
			runParitySuite(t, rn)
		})
	}
}

// runParitySuite drives one launcher through the whole pipeline once,
// asserting the daemon's API surface at three observation points: up front
// (before any node ran), while blocked at the gate (a LIVE, mid-pipeline
// run), and at terminal. Subtests are named per endpoint so a failure reads
// as e.g. "TestRunnerParity/local/stages".
func runParitySuite(t *testing.T, rn parityRunner) {
	repo := jjInitRepo(t) // colocated jj repo, cwd of the run; @ described "init"
	srv := New(rn.config(t, t.TempDir()))
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer srv.Close()

	id, err := srv.submit(parityPipeline, nil, repo, "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// /nodes: the resolved prompt of a yet-to-run node is served immediately
	// from the prepared graph — before the run has reached it — for both
	// runners. review sits past the gate, so it provably has not run.
	t.Run("nodes", func(t *testing.T) {
		resp, doc := getNode(t, srv, id, "review")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d, want 200", resp.StatusCode)
		}
		if doc.Type != "codergen" {
			t.Errorf("review type = %q, want codergen", doc.Type)
		}
		if doc.Prompt != "assess the produced change" {
			t.Errorf("review prompt = %q, want the resolved prompt served up front", doc.Prompt)
		}
		resp2, doc2 := getNode(t, srv, id, "write")
		defer resp2.Body.Close()
		if doc2.Type != "tool" {
			t.Errorf("write type = %q, want tool", doc2.Type)
		}
	})

	// Block until the run parks at the human gate. Both runners present the
	// same question via /questions (direct: the channel interviewer; local:
	// the child's forwarded InterviewStarted event).
	q := waitQuestion(t, srv, id, 30*time.Second)
	gateID, _ := q["id"].(string)
	if gateID == "" {
		t.Fatalf("gate question has no id: %v", q)
	}

	// /state: while gated the run is LIVE and its authoritative state is
	// server-derived — the gate is the sole active node (fail-aware set),
	// greet has completed, everything past the gate is still pending.
	t.Run("state", func(t *testing.T) {
		_, doc := getState(t, srv, id)
		if doc.Status != "running" {
			t.Errorf("status = %q, want running", doc.Status)
		}
		if len(doc.ActiveNodes) != 1 || doc.ActiveNodes[0] != "gate" {
			t.Errorf("active_nodes = %v, want [gate]", doc.ActiveNodes)
		}
		if g := doc.Nodes["greet"]; g.Status != "completed" || g.Attempts != 1 {
			t.Errorf("greet = %+v, want completed attempts=1", g)
		}
		for _, n := range []string{"review", "flaky", "write"} {
			if got := doc.Nodes[n].Status; got != "pending" {
				t.Errorf("%s status = %q, want pending (not yet reached)", n, got)
			}
		}
	})

	// /stages: a mid-pipeline stage that has already completed is served on a
	// LIVE run — this pins P3 (per-stage sync, not a run-end sweep) for the
	// local runner, whose stage files only exist daemon-side via phone-home
	// upload. stageDetail carries prompt/response/status/tool_calls; a tool
	// stage's verbatim stdout/stderr are stage-dir files, served (and here
	// asserted) through the artifacts endpoint.
	t.Run("stages", func(t *testing.T) {
		if got := getStageStatus(t, srv, id, "greet"); got != "success" {
			t.Errorf("greet stage status = %q, want success (served mid-run)", got)
		}
		if out := fetchArtifactText(t, srv, id, "greet/stdout.txt"); !strings.Contains(out, "greetings-42") {
			t.Errorf("greet stdout not served for a live run: %q", out)
		}
	})

	// /diff: the intentional per-runner asymmetry. direct/local stamp their
	// tip only at terminal, so a gated run's diff is not yet computable; a vm
	// runner would resolve its tip live and diverge here — which is why the
	// expected value lives in the row, asserted rather than assumed.
	t.Run("diff-live", func(t *testing.T) {
		status, _ := getDiff(t, srv, id)
		if diffStatus(status) != rn.liveDiffStatus {
			t.Errorf("live X-Diff-Status = %q, want %q for runner %q", status, rn.liveDiffStatus, rn.name)
		}
	})

	// /questions round-trip: answering the gate over the HTTP API unblocks
	// the run under BOTH runners — direct's channel interviewer and local's
	// PollInterviewer (which fetches the answer via /control) resolve it the
	// same way.
	t.Run("questions", func(t *testing.T) {
		answerGate(t, srv, id, gateID, optionKey(t, q))
	})

	run := waitTerminal(t, srv, id, 60*time.Second)
	if run.Status() != RunCompleted {
		t.Fatalf("run status = %v, want completed (failure=%q)", run.Status(), run.failure)
	}

	// /state at terminal: identical node status maps for both runners, and
	// the fail-and-retry node reports attempts=2 (the fail edge re-entered it
	// once) — the assertion naive started−completed set algebra gets wrong.
	t.Run("state-terminal", func(t *testing.T) {
		_, doc := getState(t, srv, id)
		if doc.Status != "completed" {
			t.Errorf("status = %q, want completed", doc.Status)
		}
		if len(doc.ActiveNodes) != 0 {
			t.Errorf("active_nodes = %v, want empty at terminal", doc.ActiveNodes)
		}
		for _, n := range []string{"greet", "review", "flaky", "write"} {
			if got := doc.Nodes[n].Status; got != "completed" {
				t.Errorf("%s status = %q, want completed", n, got)
			}
		}
		if f := doc.Nodes["flaky"]; f.Attempts != 2 {
			t.Errorf("flaky attempts = %d, want 2 (fail-and-retry)", f.Attempts)
		}
	})

	// /stages at terminal: the final tool node's detail — including its
	// verbatim stdout — is served for both runners.
	t.Run("stages-terminal", func(t *testing.T) {
		if got := getStageStatus(t, srv, id, "write"); got != "success" {
			t.Errorf("write stage status = %q, want success", got)
		}
		if out := fetchArtifactText(t, srv, id, "write/stdout.txt"); !strings.Contains(out, "done-writing") {
			t.Errorf("write stdout not served: %q", out)
		}
	})

	// /diff at terminal: the produced change is served with X-Diff-Status ok
	// for both runners. Polled because the tip is stamped just after the
	// launch returns, a hair after the run flips terminal (the terminal event
	// is ingested before the child process exits).
	t.Run("diff-terminal", func(t *testing.T) {
		body := waitDiffStatus(t, srv, id, diffStatusOK, 15*time.Second)
		for _, want := range []string{"result.txt", "produced-by-parity-run"} {
			if !strings.Contains(body, want) {
				t.Errorf("terminal diff missing %q:\n%s", want, body)
			}
		}
	})
}

// waitQuestion polls GET /questions until one is pending (the run parked at
// the gate) or the deadline passes.
func waitQuestion(t *testing.T, srv *Server, id string, d time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/questions")
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Questions []map[string]any `json:"questions"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if len(body.Questions) > 0 {
			return body.Questions[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pending question within %s (run never reached the gate)", d)
	return nil
}

// optionKey returns the accelerator key of a question's first option.
func optionKey(t *testing.T, q map[string]any) string {
	t.Helper()
	opts, ok := q["options"].([]any)
	if !ok || len(opts) == 0 {
		t.Fatalf("question has no options: %v", q)
	}
	first, _ := opts[0].(map[string]any)
	key, _ := first["key"].(string)
	if key == "" {
		t.Fatalf("first option has no key: %v", q)
	}
	return key
}

// answerGate posts a choice answer to the gate question and asserts the
// daemon accepted it.
func answerGate(t *testing.T, srv *Server, id, qid, key string) {
	t.Helper()
	body, _ := json.Marshal(AnswerPayload{Key: key, Label: "[A] Approve"})
	resp, err := http.Post(srv.URL()+"/pipelines/"+id+"/questions/"+qid+"/answer",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("answer status=%d body=%s", resp.StatusCode, b)
	}
}

// getStageStatus fetches GET /stages/{node} and returns its status string,
// failing the test if the stage is not served (404).
func getStageStatus(t *testing.T, srv *Server, id, node string) string {
	t.Helper()
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/stages/" + node)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stage %s not served: status=%d", node, resp.StatusCode)
	}
	var got struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	return got.Status
}

// getArtifact fetches a run artifact file (e.g. a stage's stdout.txt) as text.
func fetchArtifactText(t *testing.T, srv *Server, id, path string) string {
	t.Helper()
	resp, err := http.Get(srv.URL() + "/pipelines/" + id + "/artifacts/" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("artifact %s not served: status=%d", path, resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// waitDiffStatus polls GET /diff until X-Diff-Status equals want (or the
// deadline), returning the body. The tip commit is stamped a hair after the
// run flips terminal, so the "ok" verdict may arrive slightly late.
func waitDiffStatus(t *testing.T, srv *Server, id string, want diffStatus, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	var lastStatus, lastBody string
	for time.Now().Before(deadline) {
		lastStatus, lastBody = getDiff(t, srv, id)
		if diffStatus(lastStatus) == want {
			return lastBody
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("X-Diff-Status = %q, want %q within %s", lastStatus, want, d)
	return ""
}
