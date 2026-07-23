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

	acpbackend "github.com/fabro/attractor/internal/backend/acp"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/graph"
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
	env := engine.HandlerEnv{
		Node:     node,
		LogsRoot: t.TempDir(),
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

func TestACPBackend_EnvPrefixReachesAgent(t *testing.T) {
	b := &acpbackend.Backend{Command: fakeACPCommand(t, "FAKE_MODEL=opus-test")}
	env, _ := acpEnv(t, &graph.Node{ID: "plan", Attrs: map[string]string{}})
	result, err := b.Run(env, "hello")
	must(t, err)
	if !strings.Contains(result.ResponseText, "model=opus-test") {
		t.Fatalf("env prefix should reach the agent process, got %q", result.ResponseText)
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
			say(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":{"stopReason":"end_turn"}}`)
		}
	}
}
