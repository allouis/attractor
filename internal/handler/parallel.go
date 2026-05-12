package handler

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/graph"
)

// Parallel fans out to each outgoing edge concurrently (spec §4.8).
// Workers receive isolated context clones; only their Outcome surface
// (status + context_updates) is preserved into a `parallel.results`
// context entry. The handler then routes the engine forward to the
// nearest fan-in node (tripleoctagon) via Outcome.NextNode.
type Parallel struct{}

// Execute runs each branch through the registered handler for its
// target node, honouring max_parallel and join_policy.
func (h Parallel) Execute(env engine.HandlerEnv) engine.Outcome {
	if env.Registry == nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "parallel: env.Registry unset"}
	}
	edges := env.Graph.OutgoingEdges(env.Node.ID)
	if len(edges) == 0 {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "parallel: no outgoing edges"}
	}
	max := env.Node.Int("max_parallel", 4)
	if max < 1 {
		max = 1
	}
	policy := env.Node.Attrs["join_policy"]
	if policy == "" {
		policy = "wait_all"
	}

	emit(env, engine.Event{
		Kind:   engine.EventStageProgress,
		NodeID: env.Node.ID,
		Detail: map[string]string{
			"parallel.branch_count": fmt.Sprintf("%d", len(edges)),
			"parallel.kind":         "parallel_started",
		},
	})

	type branchResult struct {
		Branch  string         `json:"branch"`
		Outcome engine.Outcome `json:"outcome"`
		Dur     time.Duration  `json:"duration_ns"`
	}
	results := make([]branchResult, len(edges))
	sem := make(chan struct{}, max)
	var wg sync.WaitGroup
	for i, e := range edges {
		i, e := i, e
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			branchCtx := env.Context.Clone()
			workerNode, ok := env.Graph.Nodes[e.To]
			if !ok {
				results[i] = branchResult{Branch: e.To, Outcome: engine.Outcome{
					Status: engine.StatusFail, FailureReason: "parallel: worker node missing",
				}}
				return
			}
			handler, err := env.Registry.Resolve(workerNode)
			if err != nil {
				results[i] = branchResult{Branch: e.To, Outcome: engine.Outcome{
					Status: engine.StatusFail, FailureReason: "parallel: resolve handler: " + err.Error(),
				}}
				return
			}
			emit(env, engine.Event{
				Kind:    engine.EventStageProgress,
				NodeID:  env.Node.ID,
				Message: "branch_started",
				Detail:  map[string]string{"branch": e.To},
			})
			start := time.Now()
			subEnv := env
			subEnv.Node = workerNode
			subEnv.Context = branchCtx
			oc := handler.Execute(subEnv)
			oc.Finalize()
			dur := time.Since(start)
			results[i] = branchResult{Branch: e.To, Outcome: oc, Dur: dur}
			emit(env, engine.Event{
				Kind:    engine.EventStageProgress,
				NodeID:  env.Node.ID,
				Message: "branch_completed",
				Detail: map[string]string{
					"branch": e.To,
					"status": oc.Status.String(),
				},
				Duration: dur,
			})
		}()
	}
	wg.Wait()

	// Serialise results into context.
	encoded, _ := json.Marshal(results)
	successCount := 0
	failCount := 0
	for _, r := range results {
		switch r.Outcome.Status {
		case engine.StatusSuccess, engine.StatusPartialSuccess:
			successCount++
		case engine.StatusFail:
			failCount++
		}
	}

	emit(env, engine.Event{
		Kind:    engine.EventStageProgress,
		NodeID:  env.Node.ID,
		Message: "parallel_completed",
		Detail: map[string]string{
			"success_count": fmt.Sprintf("%d", successCount),
			"failure_count": fmt.Sprintf("%d", failCount),
		},
	})

	finalStatus := engine.StatusSuccess
	switch policy {
	case "first_success":
		if successCount == 0 {
			finalStatus = engine.StatusFail
		}
	default: // wait_all
		if failCount > 0 {
			finalStatus = engine.StatusPartialSuccess
		}
		if successCount == 0 {
			finalStatus = engine.StatusFail
		}
	}

	// Find the fan-in target reachable from every branch's first hop.
	joinID := findCommonDownstream(env.Graph, edges)

	updates := map[string]string{
		"parallel.results":       string(encoded),
		"parallel.success_count": fmt.Sprintf("%d", successCount),
		"parallel.failure_count": fmt.Sprintf("%d", failCount),
	}
	return engine.Outcome{
		Status:           finalStatus,
		Notes:            fmt.Sprintf("parallel: %d/%d branches succeeded", successCount, len(results)),
		ContextUpdates:   updates,
		NextNode:         joinID,
		SuggestedNextIDs: nextIDsFromEdges(edges),
	}
}

// FanIn consolidates the results recorded by a preceding Parallel
// handler (spec §4.9). The MVP implementation uses the heuristic
// selector: rank by outcome status, then by branch ID lexicographically.
type FanIn struct{}

// Execute reads parallel.results from the context, picks the best
// branch outcome, and exposes the winner via context updates so
// downstream nodes can route on it.
func (FanIn) Execute(env engine.HandlerEnv) engine.Outcome {
	raw := env.Context.Get("parallel.results")
	if raw == "" {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "fan_in: no parallel.results in context"}
	}
	type branchResult struct {
		Branch  string         `json:"branch"`
		Outcome engine.Outcome `json:"outcome"`
		Dur     time.Duration  `json:"duration_ns"`
	}
	var results []branchResult
	if err := json.Unmarshal([]byte(raw), &results); err != nil {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "fan_in: malformed parallel.results: " + err.Error()}
	}
	if len(results) == 0 {
		return engine.Outcome{Status: engine.StatusFail, FailureReason: "fan_in: no candidates"}
	}
	rank := map[engine.Status]int{
		engine.StatusSuccess:        0,
		engine.StatusPartialSuccess: 1,
		engine.StatusRetry:          2,
		engine.StatusFail:           3,
	}
	for i := range results {
		results[i].Outcome.Status = engine.ParseStatus(results[i].Outcome.StatusString)
	}
	sort.SliceStable(results, func(i, j int) bool {
		ri := rank[results[i].Outcome.Status]
		rj := rank[results[j].Outcome.Status]
		if ri != rj {
			return ri < rj
		}
		return results[i].Branch < results[j].Branch
	})
	best := results[0]
	if best.Outcome.Status == engine.StatusFail {
		return engine.Outcome{
			Status:        engine.StatusFail,
			FailureReason: "fan_in: all candidates failed",
			ContextUpdates: map[string]string{
				"parallel.fan_in.best_id": best.Branch,
			},
		}
	}
	return engine.Outcome{
		Status: engine.StatusSuccess,
		Notes:  "Selected best candidate: " + best.Branch,
		ContextUpdates: map[string]string{
			"parallel.fan_in.best_id":      best.Branch,
			"parallel.fan_in.best_outcome": best.Outcome.Status.String(),
		},
	}
}

// findCommonDownstream walks one hop forward from each edge target and
// returns the first node ID common to all of them. Empty string when no
// common downstream exists (in which case the engine falls back to
// normal edge selection from the parallel node — usually a config bug).
func findCommonDownstream(g *graph.Graph, branches []*graph.Edge) string {
	if len(branches) == 0 {
		return ""
	}
	// Build first-hop set for the first branch, then intersect.
	common := downstreamSet(g, branches[0].To)
	for _, e := range branches[1:] {
		next := downstreamSet(g, e.To)
		for id := range common {
			if !next[id] {
				delete(common, id)
			}
		}
	}
	if len(common) == 0 {
		return ""
	}
	// Prefer a tripleoctagon (fan_in) match.
	for id := range common {
		if g.Nodes[id].Shape() == "tripleoctagon" {
			return id
		}
	}
	// Else lexicographically smallest for determinism.
	var keys []string
	for id := range common {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	return keys[0]
}

func downstreamSet(g *graph.Graph, from string) map[string]bool {
	out := map[string]bool{}
	for _, e := range g.OutgoingEdges(from) {
		out[e.To] = true
	}
	return out
}

func nextIDsFromEdges(edges []*graph.Edge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.To)
	}
	return out
}

func emit(env engine.HandlerEnv, ev engine.Event) {
	if env.Emit != nil {
		env.Emit(ev)
	}
}
