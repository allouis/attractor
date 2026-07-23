package attractor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/fabro/attractor/internal/acp"
)

// connPeer is a scripted JSON-RPC peer on the far side of an in-process
// pipe pair. Tests read requests off `in` and write raw lines to `out`.
type connPeer struct {
	conn *acp.Conn
	in   *bufio.Scanner
	out  io.Writer
}

func newConnPeer(t *testing.T) *connPeer {
	t.Helper()
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	conn := acp.NewConn(toPeerW, fromPeerR)
	t.Cleanup(func() {
		conn.Close()
		toPeerR.Close()
		fromPeerW.Close()
	})
	return &connPeer{conn: conn, in: bufio.NewScanner(toPeerR), out: fromPeerW}
}

// readMsg returns the next JSON line the conn wrote.
func (p *connPeer) readMsg(t *testing.T) map[string]any {
	t.Helper()
	if !p.in.Scan() {
		t.Fatalf("peer: no line available: %v", p.in.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(p.in.Bytes(), &m); err != nil {
		t.Fatalf("peer: bad JSON line %q: %v", p.in.Text(), err)
	}
	return m
}

func (p *connPeer) writeLine(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(p.out, line+"\n"); err != nil {
		t.Fatalf("peer write: %v", err)
	}
}

func TestACPConn_CallRoundTrip(t *testing.T) {
	p := newConnPeer(t)

	type initResult struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	got := make(chan initResult, 1)
	errc := make(chan error, 1)
	go func() {
		var res initResult
		err := p.conn.Call(context.Background(), "initialize",
			map[string]any{"protocolVersion": 1}, &res)
		errc <- err
		got <- res
	}()

	req := p.readMsg(t)
	if req["jsonrpc"] != "2.0" {
		t.Fatalf("missing jsonrpc version: %v", req)
	}
	if req["method"] != "initialize" {
		t.Fatalf("wrong method: %v", req)
	}
	params := req["params"].(map[string]any)
	if params["protocolVersion"] != float64(1) {
		t.Fatalf("params not forwarded: %v", req)
	}
	id := req["id"]
	raw, _ := json.Marshal(id)
	p.writeLine(t, `{"jsonrpc":"2.0","id":`+string(raw)+`,"result":{"protocolVersion":1}}`)

	if err := <-errc; err != nil {
		t.Fatalf("call failed: %v", err)
	}
	if res := <-got; res.ProtocolVersion != 1 {
		t.Fatalf("result not decoded: %+v", res)
	}
}

func TestACPConn_CallErrorResponse(t *testing.T) {
	p := newConnPeer(t)

	errc := make(chan error, 1)
	go func() {
		errc <- p.conn.Call(context.Background(), "session/new", nil, nil)
	}()
	req := p.readMsg(t)
	raw, _ := json.Marshal(req["id"])
	p.writeLine(t, `{"jsonrpc":"2.0","id":`+string(raw)+`,"error":{"code":-32601,"message":"method not found"}}`)

	err := <-errc
	if err == nil {
		t.Fatal("expected error from error response")
	}
	var rpcErr *acp.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("want *acp.RPCError, got %T: %v", err, err)
	}
	if rpcErr.Code != -32601 || rpcErr.Message != "method not found" {
		t.Fatalf("error fields wrong: %+v", rpcErr)
	}
}

func TestACPConn_IncomingRequestDispatched(t *testing.T) {
	p := newConnPeer(t)

	p.conn.OnRequest("session/request_permission", func(params json.RawMessage) (any, error) {
		var in struct {
			Tool string `json:"tool"`
		}
		_ = json.Unmarshal(params, &in)
		return map[string]any{"granted": in.Tool}, nil
	})

	p.writeLine(t, `{"jsonrpc":"2.0","id":"agent-1","method":"session/request_permission","params":{"tool":"bash"}}`)
	resp := p.readMsg(t)
	if resp["id"] != "agent-1" {
		t.Fatalf("response id mismatch: %v", resp)
	}
	result := resp["result"].(map[string]any)
	if result["granted"] != "bash" {
		t.Fatalf("handler result not returned: %v", resp)
	}
}

func TestACPConn_UnknownIncomingRequestGetsMethodNotFound(t *testing.T) {
	p := newConnPeer(t)
	p.writeLine(t, `{"jsonrpc":"2.0","id":7,"method":"fs/read_text_file","params":{}}`)
	resp := p.readMsg(t)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error response, got %v", resp)
	}
	if errObj["code"] != float64(-32601) {
		t.Fatalf("expected -32601 method not found, got %v", resp)
	}
}

func TestACPConn_NotificationsBothWays(t *testing.T) {
	p := newConnPeer(t)

	got := make(chan string, 1)
	p.conn.OnNotification("session/update", func(params json.RawMessage) {
		var in struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(params, &in)
		got <- in.Text
	})
	p.writeLine(t, `{"jsonrpc":"2.0","method":"session/update","params":{"text":"hi"}}`)
	select {
	case text := <-got:
		if text != "hi" {
			t.Fatalf("notification params wrong: %q", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification never dispatched")
	}

	// io.Pipe writes block until read, so Notify must run concurrently
	// with the peer-side read below.
	notifyErr := make(chan error, 1)
	go func() {
		notifyErr <- p.conn.Notify("session/cancel", map[string]any{"sessionId": "s1"})
	}()
	out := p.readMsg(t)
	if err := <-notifyErr; err != nil {
		t.Fatalf("notify: %v", err)
	}
	if out["method"] != "session/cancel" {
		t.Fatalf("outgoing notification wrong: %v", out)
	}
	if _, hasID := out["id"]; hasID {
		t.Fatalf("notification must not carry an id: %v", out)
	}
}

func TestACPConn_PeerEOFFailsPendingCalls(t *testing.T) {
	toPeerR, toPeerW := io.Pipe()
	fromPeerR, fromPeerW := io.Pipe()
	conn := acp.NewConn(toPeerW, fromPeerR)
	t.Cleanup(func() { conn.Close(); toPeerR.Close() })

	errc := make(chan error, 1)
	go func() {
		errc <- conn.Call(context.Background(), "session/prompt", nil, nil)
	}()
	// Drain the request, then close the peer's write side (agent died).
	drain := bufio.NewScanner(toPeerR)
	if !drain.Scan() {
		t.Fatal("no request written")
	}
	fromPeerW.Close()

	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected error after peer EOF")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not fail after peer EOF")
	}
}

func TestACPConn_CallHonoursContextCancel(t *testing.T) {
	p := newConnPeer(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- p.conn.Call(ctx, "session/prompt", nil, nil)
	}()
	p.readMsg(t) // request sent, never answered
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("call did not return on context cancel")
	}
}
