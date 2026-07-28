package attractor_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/allouis/attractor/internal/acp"
)

// TestACPClient_LifecycleAndUpdates drives the full happy path:
// initialize → session/new → session/prompt with streamed updates.
func TestACPClient_LifecycleAndUpdates(t *testing.T) {
	p := newConnPeer(t)

	var mu sync.Mutex
	var updates []acp.SessionUpdate
	client := acp.NewClient(p.conn, acp.ClientConfig{
		OnUpdate: func(u acp.SessionUpdate) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, u)
		},
	})

	type promptOut struct {
		stop acp.StopReason
		err  error
	}
	done := make(chan promptOut, 1)
	go func() {
		if _, err := client.Initialize(context.Background()); err != nil {
			done <- promptOut{err: err}
			return
		}
		sid, err := client.NewSession(context.Background(), "/work/dir")
		if err != nil {
			done <- promptOut{err: err}
			return
		}
		if sid != "sess-1" {
			t.Errorf("session id = %q, want sess-1", sid)
		}
		stop, err := client.Prompt(context.Background(), sid, "fix the bug")
		done <- promptOut{stop: stop, err: err}
	}()

	// --- initialize ---
	init := p.readMsg(t)
	if init["method"] != "initialize" {
		t.Fatalf("first request should be initialize, got %v", init)
	}
	params := init["params"].(map[string]any)
	if params["protocolVersion"] != float64(1) {
		t.Fatalf("initialize must send protocolVersion 1: %v", params)
	}
	caps := params["clientCapabilities"].(map[string]any)
	fs := caps["fs"].(map[string]any)
	if fs["readTextFile"] != false || fs["writeTextFile"] != false {
		t.Fatalf("client must advertise no fs capabilities: %v", caps)
	}
	id, _ := json.Marshal(init["id"])
	p.writeLine(t, `{"jsonrpc":"2.0","id":`+string(id)+`,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}`)

	// --- session/new ---
	newSess := p.readMsg(t)
	if newSess["method"] != "session/new" {
		t.Fatalf("expected session/new, got %v", newSess)
	}
	if newSess["params"].(map[string]any)["cwd"] != "/work/dir" {
		t.Fatalf("session/new must carry cwd: %v", newSess)
	}
	id, _ = json.Marshal(newSess["id"])
	p.writeLine(t, `{"jsonrpc":"2.0","id":`+string(id)+`,"result":{"sessionId":"sess-1"}}`)

	// --- session/prompt with interleaved updates ---
	prompt := p.readMsg(t)
	if prompt["method"] != "session/prompt" {
		t.Fatalf("expected session/prompt, got %v", prompt)
	}
	pp := prompt["params"].(map[string]any)
	if pp["sessionId"] != "sess-1" {
		t.Fatalf("prompt must target the session: %v", pp)
	}
	blocks := pp["prompt"].([]any)
	first := blocks[0].(map[string]any)
	if first["type"] != "text" || first["text"] != "fix the bug" {
		t.Fatalf("prompt content block wrong: %v", pp)
	}

	p.writeLine(t, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"working on it"}}}}`)
	p.writeLine(t, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-1","title":"Edit main.go","kind":"edit","status":"pending"}}}`)
	p.writeLine(t, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc-1","status":"completed"}}}`)
	id, _ = json.Marshal(prompt["id"])
	p.writeLine(t, `{"jsonrpc":"2.0","id":`+string(id)+`,"result":{"stopReason":"end_turn"}}`)

	out := <-done
	if out.err != nil {
		t.Fatalf("client flow failed: %v", out.err)
	}
	if out.stop != acp.StopEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", out.stop)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) != 3 {
		t.Fatalf("expected 3 updates, got %d: %+v", len(updates), updates)
	}
	if updates[0].Kind != "agent_message_chunk" || updates[0].Text != "working on it" {
		t.Fatalf("first update wrong: %+v", updates[0])
	}
	if updates[1].Kind != "tool_call" || updates[1].ToolCall == nil ||
		updates[1].ToolCall.ID != "tc-1" || updates[1].ToolCall.Title != "Edit main.go" {
		t.Fatalf("tool_call update wrong: %+v", updates[1])
	}
	if updates[2].Kind != "tool_call_update" || updates[2].ToolCall == nil ||
		updates[2].ToolCall.Status != "completed" {
		t.Fatalf("tool_call_update wrong: %+v", updates[2])
	}
}

// TestACPClient_ParsesUsageUpdate checks a session/update carrying a
// token-usage payload surfaces on SessionUpdate.Usage, and that updates
// without usage leave it nil.
func TestACPClient_ParsesUsageUpdate(t *testing.T) {
	p := newConnPeer(t)

	var mu sync.Mutex
	var updates []acp.SessionUpdate
	acp.NewClient(p.conn, acp.ClientConfig{
		OnUpdate: func(u acp.SessionUpdate) {
			mu.Lock()
			defer mu.Unlock()
			updates = append(updates, u)
		},
	})

	p.writeLine(t, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"usage","usage":{"inputTokens":1200,"outputTokens":340}}}}`)
	p.writeLine(t, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}`)

	// Poll until both updates land (dispatch is async on the read loop).
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(updates)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(updates) < 2 {
		t.Fatalf("expected 2 updates, got %d: %+v", len(updates), updates)
	}
	if updates[0].Usage == nil {
		t.Fatalf("usage update should populate Usage, got %+v", updates[0])
	}
	if updates[0].Usage.InputTokens != 1200 || updates[0].Usage.OutputTokens != 340 {
		t.Fatalf("usage tokens wrong: %+v", updates[0].Usage)
	}
	if updates[1].Usage != nil {
		t.Fatalf("non-usage update must leave Usage nil, got %+v", updates[1].Usage)
	}
}

// TestACPClient_InitializeReportsLoadSession checks the loadSession
// agent capability surfaces on the InitializeResult.
func TestACPClient_InitializeReportsLoadSession(t *testing.T) {
	p := newConnPeer(t)
	client := acp.NewClient(p.conn, acp.ClientConfig{})

	type initOut struct {
		res acp.InitializeResult
		err error
	}
	done := make(chan initOut, 1)
	go func() {
		res, err := client.Initialize(context.Background())
		done <- initOut{res, err}
	}()
	req := p.readMsg(t)
	id, _ := json.Marshal(req["id"])
	p.writeLine(t, `{"jsonrpc":"2.0","id":`+string(id)+`,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":false}}}`)
	out := <-done
	if out.err != nil {
		t.Fatalf("initialize: %v", out.err)
	}
	if out.res.SupportsLoadSession {
		t.Fatal("loadSession=false must not report support")
	}
}

// TestACPClient_PermissionAutoAllow verifies the auto-responder picks
// allow_always over allow_once over any other non-reject option.
func TestACPClient_PermissionAutoAllow(t *testing.T) {
	p := newConnPeer(t)
	observed := make(chan string, 1)
	acp.NewClient(p.conn, acp.ClientConfig{
		OnPermission: func(req acp.PermissionRequest, chosen string) {
			observed <- chosen
		},
	})

	p.writeLine(t, `{"jsonrpc":"2.0","id":"perm-1","method":"session/request_permission","params":{"sessionId":"sess-1","toolCall":{"toolCallId":"tc-9","title":"Run tests"},"options":[{"optionId":"opt-reject","name":"No","kind":"reject_once"},{"optionId":"opt-once","name":"Once","kind":"allow_once"},{"optionId":"opt-always","name":"Always","kind":"allow_always"}]}}`)
	resp := p.readMsg(t)
	outcome := resp["result"].(map[string]any)["outcome"].(map[string]any)
	if outcome["outcome"] != "selected" || outcome["optionId"] != "opt-always" {
		t.Fatalf("expected allow_always selection, got %v", resp)
	}
	if chosen := <-observed; chosen != "opt-always" {
		t.Fatalf("OnPermission observed %q, want opt-always", chosen)
	}
}

// TestACPClient_PermissionOnlyRejectCancels: nothing grantable → the
// request resolves as cancelled rather than picking a reject option.
func TestACPClient_PermissionOnlyRejectCancels(t *testing.T) {
	p := newConnPeer(t)
	acp.NewClient(p.conn, acp.ClientConfig{})

	p.writeLine(t, `{"jsonrpc":"2.0","id":"perm-2","method":"session/request_permission","params":{"sessionId":"sess-1","options":[{"optionId":"opt-no","name":"No","kind":"reject_once"}]}}`)
	resp := p.readMsg(t)
	outcome := resp["result"].(map[string]any)["outcome"].(map[string]any)
	if outcome["outcome"] != "cancelled" {
		t.Fatalf("expected cancelled outcome, got %v", resp)
	}
}

// TestACPClient_CancelSendsNotification checks Cancel emits the
// session/cancel notification (no id).
func TestACPClient_CancelSendsNotification(t *testing.T) {
	p := newConnPeer(t)
	client := acp.NewClient(p.conn, acp.ClientConfig{})

	errc := make(chan error, 1)
	go func() { errc <- client.Cancel("sess-1") }()
	msg := p.readMsg(t)
	if err := <-errc; err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if msg["method"] != "session/cancel" {
		t.Fatalf("expected session/cancel, got %v", msg)
	}
	if _, hasID := msg["id"]; hasID {
		t.Fatalf("cancel must be a notification: %v", msg)
	}
	if msg["params"].(map[string]any)["sessionId"] != "sess-1" {
		t.Fatalf("cancel must name the session: %v", msg)
	}
}

// TestACPClient_LoadSession checks LoadSession sends session/load with
// the session id and cwd.
func TestACPClient_LoadSession(t *testing.T) {
	p := newConnPeer(t)
	client := acp.NewClient(p.conn, acp.ClientConfig{})

	errc := make(chan error, 1)
	go func() {
		errc <- client.LoadSession(context.Background(), "sess-old", "/work/dir")
	}()
	req := p.readMsg(t)
	if req["method"] != "session/load" {
		t.Fatalf("expected session/load, got %v", req)
	}
	params := req["params"].(map[string]any)
	if params["sessionId"] != "sess-old" || params["cwd"] != "/work/dir" {
		t.Fatalf("session/load params wrong: %v", req)
	}
	id, _ := json.Marshal(req["id"])
	p.writeLine(t, `{"jsonrpc":"2.0","id":`+string(id)+`,"result":null}`)
	if err := <-errc; err != nil {
		t.Fatalf("load session: %v", err)
	}
}
