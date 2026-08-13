package handler

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/allouis/attractor/internal/backend"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

// Contract checks (local-first plan D2): deterministic post-execution
// checks that verify the agent's *claims* about its work. A violation
// carries a correction prompt the handler sends as a continuation turn
// in the same session — bounded, then the node fails loud. Corrections
// apply to contract violations only, never to real task failures.

// Violation is a failed contract check plus the prompt that asks the
// agent to fix it in-session.
type Violation struct {
	Check            string
	Message          string
	CorrectionPrompt string
}

// ContractCheck is one deterministic post-execution check. Check
// returns nil on pass.
type ContractCheck interface {
	Name() string
	Check(env engine.HandlerEnv, outcome engine.Outcome) *Violation
}

// maxContractCorrections bounds in-session correction turns per node
// execution before the node fails loud.
const maxContractCorrections = 2

// contractChecks returns the checks that apply to the node. v1 ships
// one: the status-file check (honoring require_status). Future checks
// (declared artifacts exist, claimed tests re-run green) slot in here
// without touching the correction loop.
func contractChecks(node *graph.Node) []ContractCheck {
	return []ContractCheck{statusFileCheck{}}
}

// firstViolation runs the node's checks in order and returns the first
// violation, or nil when all pass.
func firstViolation(env engine.HandlerEnv, outcome engine.Outcome) *Violation {
	for _, c := range contractChecks(env.Node) {
		if v := c.Check(env, outcome); v != nil {
			return v
		}
	}
	return nil
}

// statusFileCheck enforces the status-file contract on require_status
// nodes: {stage_dir}/status.json must exist and parse to a recognized
// outcome. Nodes without require_status pass vacuously (a missing
// status there legitimately synthesises SUCCESS).
type statusFileCheck struct{}

func (statusFileCheck) Name() string { return "status_file" }

func (statusFileCheck) Check(env engine.HandlerEnv, _ engine.Outcome) *Violation {
	if env.Node == nil || !env.Node.Bool("require_status") || env.Stage == nil {
		return nil
	}
	if _, ok := readAgentStatus(env.Stage); ok {
		return nil
	}
	reason := requireStatusMissReason(env.Stage)
	path := filepath.Join(env.Stage.Root(), "status.json")
	return &Violation{
		Check:   "status_file",
		Message: reason,
		CorrectionPrompt: fmt.Sprintf(
			"Your status report is missing or invalid: %s\n\n"+
				"Write %s now. It must be a JSON object reporting the result of the work you already did in this session, for example:\n"+
				"  {\"outcome\": \"success\"}\n"+
				"or\n"+
				"  {\"outcome\": \"fail\", \"failure_reason\": \"<why>\"}\n\n"+
				"Report honestly on the work you already completed. Do not redo the task.",
			reason, path),
	}
}

// enforceContracts runs the node's contract checks against the resolved
// outcome. On violation, a backend that supports in-session
// continuation gets up to maxContractCorrections corrective turns, the
// outcome re-resolved after each; a violation that survives (or a
// backend with no Continuer) fails the node loud with the violation
// message.
func (h Codergen) enforceContracts(env engine.HandlerEnv, stage *runstore.Dir, outcome engine.Outcome, response string) engine.Outcome {
	v := firstViolation(env, outcome)
	if v == nil {
		return outcome
	}
	cont, ok := h.Backend.(backend.Continuer)
	if !ok {
		return violationFail(env, v, response)
	}
	for i := 1; i <= maxContractCorrections; i++ {
		if env.Emit != nil {
			env.Emit(engine.Event{
				Kind:   engine.EventStageProgress,
				NodeID: env.Node.ID,
				Message: fmt.Sprintf("contract violation (%s), correction %d/%d: %s",
					v.Check, i, maxContractCorrections, v.Message),
				Detail: map[string]string{
					"kind":    "contract_correction",
					"check":   v.Check,
					"attempt": strconv.Itoa(i),
				},
			})
		}
		_ = stage.Write(fmt.Sprintf("correction-%d.prompt.md", i), []byte(v.CorrectionPrompt))
		workStatus, workStatusExisted := leakedStatusProbe(env)
		result, err := cont.Continue(env, v.CorrectionPrompt)
		relocateLeakedStatus(stage, workStatus, workStatusExisted)
		if err != nil {
			return backendErrOutcome(err)
		}
		if result.ResponseText != "" {
			_ = stage.Write(fmt.Sprintf("correction-%d.response.md", i), []byte(result.ResponseText))
		}
		if result.Outcome != nil {
			outcome = *result.Outcome
		} else {
			outcome = resolveTextOutcome(env, stage, response)
		}
		v = firstViolation(env, outcome)
		if v == nil {
			return outcome
		}
	}
	return violationFail(env, v, response)
}

// violationFail is the terminal outcome for an uncorrected contract
// violation: a machinery-classed RETRY (loop-guards LG1). The engine
// re-runs the node under its retry policy; exhausted, the run fails
// with the violation message — never routing the harness error to a
// fix agent. The message keeps the violation signature (see
// isRequireStatusMiss) so consumers still recognize a harness miss.
func violationFail(env engine.HandlerEnv, v *Violation, response string) engine.Outcome {
	return engine.Outcome{
		Status:         engine.StatusRetry,
		FailureReason:  v.Message,
		FailureClass:   engine.FailureClassMachinery,
		ContextUpdates: applyDefaults(nil, env.Node, response),
	}
}
