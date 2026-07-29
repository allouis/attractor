package attractor_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	acpbackend "github.com/allouis/attractor/internal/backend/acp"
	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/graph"
	"github.com/allouis/attractor/internal/runstore"
)

// fakeACPCommand returns a command line that re-invokes this test
// binary as a scripted ACP agent (see TestFakeACPAgentProcess).
// Extra tokens are appended as leading ENV=val assignments.
func fakeACPCommand(t *testing.T, envPrefix ...string) string {
	t.Helper()
	exe, err := os.Executable()
	must(t, err)
	parts := append([]string{"FAKE_ACP_AGENT=1"}, envPrefix...)
	parts = append(parts, exe, "-test.run=TestFakeACPAgentProcess")
	return strings.Join(parts, " ")
}

// acpEnv builds a minimal HandlerEnv for direct Backend.Run calls and
// returns the env plus the collected-events accessor.
func acpEnv(t *testing.T, node *graph.Node) (engine.HandlerEnv, func() []engine.Event) {
	t.Helper()
	var mu sync.Mutex
	var events []engine.Event
	logsRoot := t.TempDir()
	env := engine.HandlerEnv{
		Node:     node,
		LogsRoot: logsRoot,
		Stage:    runstore.New(logsRoot).Sub(node.ID),
		RunID:    "test-run",
		Cwd:      t.TempDir(),
		Emit: func(ev engine.Event) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, ev)
		},
	}
	return env, func() []engine.Event {
		mu.Lock()
		defer mu.Unlock()
		return append([]engine.Event(nil), events...)
	}
}

func TestACPBackend_RunHappyPath(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t)}
	env, getEvents := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})

	result, err := b.Run(env, "fix the bug")
	must(t, err)
	if result.Outcome != nil {
		t.Fatalf("happy path should not force an outcome: %+v", result.Outcome)
	}
	if !strings.Contains(result.ResponseText, "echo:fix the bug") {
		t.Fatalf("response should carry agent text, got %q", result.ResponseText)
	}

	events := getEvents()
	var sawDelta, sawToolCall bool
	for _, ev := range events {
		if ev.Kind != engine.EventStageProgress {
			continue
		}
		switch ev.Detail["kind"] {
		case "assistant_delta":
			sawDelta = true
		case "tool_call":
			sawToolCall = true
			if ev.Detail["tool_call_id"] == "" {
				t.Fatalf("tool_call event missing id: %+v", ev)
			}
		}
	}
	if !sawDelta || !sawToolCall {
		t.Fatalf("expected assistant_delta and tool_call progress events, got %+v", events)
	}

	// Tool calls persisted for post-mortem, parity with the hookshim flow.
	entries, err := os.ReadDir(filepath.Join(env.LogsRoot, "plan", "tool_calls"))
	must(t, err)
	if len(entries) == 0 {
		t.Fatal("expected persisted tool_call payloads")
	}
}

func TestACPBackend_EmitsUsageEvent(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t)}
	env, getEvents := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})

	_, err := b.Run(env, "hello")
	must(t, err)

	var usage *engine.Usage
	var count int
	for _, ev := range getEvents() {
		if ev.Kind != engine.EventUsage {
			continue
		}
		count++
		usage = ev.Usage
		if ev.NodeID != "plan" {
			t.Fatalf("usage event should carry the node id, got %q", ev.NodeID)
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one usage event, got %d", count)
	}
	if usage == nil || usage.InputTokens != 1200 || usage.OutputTokens != 340 {
		t.Fatalf("usage event tokens wrong: %+v", usage)
	}
}

func TestACPBackend_EnvPrefixReachesAgent(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t, "FAKE_MODEL=opus-test")}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	result, err := b.Run(env, "hello")
	must(t, err)
	if !strings.Contains(result.ResponseText, "model=opus-test") {
		t.Fatalf("env prefix should reach the agent process, got %q", result.ResponseText)
	}
}

func TestACPBackend_ModelEnvInjectsLLMModel(t *testing.T) {
	// ModelEnv names the env var; the node's llm_model is injected
	// through it per Run so one cached per-provider backend serves nodes
	// with different models.
	b := &acpbackend.Backend{Command: fakeACPCommand(t), ModelEnv: "FAKE_MODEL"}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{
		"llm_model": "opus-test",
	}})
	result, err := b.Run(env, "hello")
	must(t, err)
	if !strings.Contains(result.ResponseText, "model=opus-test") {
		t.Fatalf("llm_model should reach the agent via ModelEnv, got %q", result.ResponseText)
	}
}

func TestACPBackend_ModelEnvAppliesWithNodeCommandOverride(t *testing.T) {
	// Model injection is orthogonal to command precedence: a node
	// acp_command still wins for the command while ModelEnv/llm_model
	// carry the model.
	b := &acpbackend.Backend{Command: "/nonexistent/agent-binary", ModelEnv: "FAKE_MODEL"}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{
		"acp_command": fakeACPCommand(t),
		"llm_model":   "opus-test",
	}})
	result, err := b.Run(env, "hello")
	must(t, err)
	if !strings.Contains(result.ResponseText, "model=opus-test") {
		t.Fatalf("model should reach agent even when node overrides command, got %q", result.ResponseText)
	}
}

func TestACPBackend_NodeAttrOverridesCommand(t *testing.T) {
	b := &acpbackend.Backend{Command: "/nonexistent/agent-binary"}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{
		"acp_command": fakeACPCommand(t),
	}})
	result, err := b.Run(env, "hello")
	must(t, err)
	if !strings.Contains(result.ResponseText, "echo:hello") {
		t.Fatalf("node acp_command should win over Backend.Command, got %q", result.ResponseText)
	}
}

func TestACPBackend_GraphAttrIsFallback(t *testing.T) {
	b := &acpbackend.Backend{}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	env.Graph = &graph.Graph{Attrs: map[string]string{"acp_command": fakeACPCommand(t)}}
	result, err := b.Run(env, "hello")
	must(t, err)
	if !strings.Contains(result.ResponseText, "echo:hello") {
		t.Fatalf("graph acp_command should apply when node has none, got %q", result.ResponseText)
	}
}

func TestACPBackend_NoCommandErrors(t *testing.T) {
	b := &acpbackend.Backend{}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	_, err := b.Run(env, "hello")
	if err == nil {
		t.Fatal("expected error when no agent command is configured")
	}
	if !strings.Contains(err.Error(), "acp_command") {
		t.Fatalf("error should mention acp_command, got %v", err)
	}
}

func TestACPBackend_RefusalFailsStage(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t, "FAKE_ACP_MODE=refuse")}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	result, err := b.Run(env, "hello")
	must(t, err)
	if result.Outcome == nil || result.Outcome.Status != engine.StatusFail {
		t.Fatalf("refusal should produce FAIL outcome, got %+v", result)
	}
}

func TestACPBackend_AgentDeathIsError(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t, "FAKE_ACP_MODE=die")}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	_, err := b.Run(env, "hello")
	if err == nil {
		t.Fatal("expected error when agent dies mid-turn")
	}
}

func TestACPBackend_NodeTimeoutAttr(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t, "FAKE_ACP_MODE=hang")}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{
		"timeout": "500ms",
	}})
	start := time.Now()
	_, err := b.Run(env, "hello")
	if err == nil {
		t.Fatal("expected timeout error from node timeout attr")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("node timeout not honoured, took %v", time.Since(start))
	}
}

func TestACPBackend_StallTimeoutFires(t *testing.T) {
	// hang sends no updates after the prompt; the stall watchdog should
	// kill the turn well before the process's 30s sleep.
	b := &acpbackend.Backend{
		Command:      fakeACPCommand(t, "FAKE_ACP_MODE=hang"),
		StallTimeout: 300 * time.Millisecond,
	}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	start := time.Now()
	_, err := b.Run(env, "hello")
	if err == nil {
		t.Fatal("expected stall error when the agent goes quiet")
	}
	if !strings.Contains(err.Error(), "stall") {
		t.Fatalf("error should mention stall, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("stall watchdog did not fire promptly, took %v", time.Since(start))
	}
}

func TestACPBackend_StallTimeoutNodeAttr(t *testing.T) {
	// The node stall_timeout attribute overrides the backend default.
	b := &acpbackend.Backend{
		Command:      fakeACPCommand(t, "FAKE_ACP_MODE=hang"),
		StallTimeout: time.Hour,
	}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{
		"stall_timeout": "300ms",
	}})
	start := time.Now()
	_, err := b.Run(env, "hello")
	if err == nil {
		t.Fatal("expected stall error from node stall_timeout attr")
	}
	if !strings.Contains(err.Error(), "stall") {
		t.Fatalf("error should mention stall, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("node stall_timeout not honoured, took %v", time.Since(start))
	}
}

func TestACPBackend_StallTimeoutNoFalsePositive(t *testing.T) {
	// A normal turn streams updates and finishes quickly; a generous
	// stall timeout must not trip it.
	b := &acpbackend.Backend{
		Command:      fakeACPCommand(t),
		StallTimeout: 10 * time.Second,
	}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	result, err := b.Run(env, "hello")
	must(t, err)
	if result.Outcome != nil {
		t.Fatalf("happy path must not force an outcome: %+v", result.Outcome)
	}
	if !strings.Contains(result.ResponseText, "echo:hello") {
		t.Fatalf("expected agent text, got %q", result.ResponseText)
	}
}

func TestACPBackend_TimeoutKillsAgent(t *testing.T) {
	b := &acpbackend.Backend{
		Command: fakeACPCommand(t, "FAKE_ACP_MODE=hang"),
		Timeout: 500 * time.Millisecond,
	}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	start := time.Now()
	_, err := b.Run(env, "hello")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("timeout took too long: %v", time.Since(start))
	}
}

// ---------------------------------------------------------------------------

// TestFakeACPAgentProcess is not a test: it is the scripted ACP agent
// the backend tests spawn, selected via -test.run when this test
// binary is re-invoked with FAKE_ACP_AGENT=1.
func TestFakeACPAgentProcess(t *testing.T) {
	if os.Getenv("FAKE_ACP_AGENT") != "1" {
		t.Skip("helper process, not a test")
	}
	runFakeACPAgent()
}

func runFakeACPAgent() {
	mode := os.Getenv("FAKE_ACP_MODE")
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	say := func(line string) { fmt.Println(line) }
	loadedSession := ""
	for in.Scan() {
		var m map[string]any
		if err := json.Unmarshal(in.Bytes(), &m); err != nil {
			continue
		}
		method, _ := m["method"].(string)
		id, _ := json.Marshal(m["id"])
		switch method {
		case "initialize":
			loadSession := "true"
			if mode == "noload" {
				loadSession = "false"
			}
			say(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":` + loadSession + `}}}`)
		case "session/new":
			say(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"sessionId":"fake-sess-1"}}`)
		case "session/load":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			raw, _ := json.Marshal(m["params"])
			_ = json.Unmarshal(raw, &p)
			loadedSession = p.SessionID
			say(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":null}`)
		case "session/prompt":
			if mode == "die" {
				os.Exit(1)
			}
			if mode == "hang" {
				time.Sleep(30 * time.Second)
				os.Exit(1)
			}
			// Ask permission first; the client must auto-grant.
			say(`{"jsonrpc":"2.0","id":"perm-1","method":"session/request_permission","params":{"sessionId":"fake-sess-1","toolCall":{"toolCallId":"tc-1","title":"Edit file"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"},{"optionId":"deny","name":"Deny","kind":"reject_once"}]}}`)
			if !in.Scan() {
				os.Exit(1)
			}
			var permResp map[string]any
			_ = json.Unmarshal(in.Bytes(), &permResp)
			outcome, _ := permResp["result"].(map[string]any)["outcome"].(map[string]any)
			if outcome["outcome"] != "selected" || outcome["optionId"] != "allow" {
				say(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"stopReason":"refusal"}}`)
				continue
			}
			if mode == "refuse" {
				say(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"stopReason":"refusal"}}`)
				continue
			}
			// Echo the prompt text back, tagged with the model env so
			// tests can confirm env prefixes reached the process.
			var params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			raw, _ := json.Marshal(m["params"])
			_ = json.Unmarshal(raw, &params)
			promptText := ""
			if len(params.Prompt) > 0 {
				promptText = params.Prompt[0].Text
			}
			reply := "echo:" + promptText
			if model := os.Getenv("FAKE_MODEL"); model != "" {
				reply += " model=" + model
			}
			if loadedSession != "" {
				reply += " resumed=" + loadedSession
			}
			chunk, _ := json.Marshal(reply)
			say(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fake-sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + string(chunk) + `}}}}`)
			say(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fake-sess-1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"Edit file","kind":"edit","status":"in_progress"}}}`)
			say(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fake-sess-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed"}}}`)
			say(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fake-sess-1","update":{"sessionUpdate":"usage","usage":{"inputTokens":1200,"outputTokens":340}}}}`)
			say(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"stopReason":"end_turn"}}`)
		}
	}
}
