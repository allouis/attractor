// Package acp implements the client side of the Agent Client Protocol
// (https://agentclientprotocol.com): newline-delimited JSON-RPC 2.0
// over a byte stream, plus the ACP session lifecycle on top. The
// transport is deliberately minimal — attractor only needs the client
// role, so this is not a general-purpose JSON-RPC library.
package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// RPCError is a JSON-RPC 2.0 error object returned by the peer.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

const codeMethodNotFound = -32601

// RequestHandler serves an incoming peer request; the returned value is
// marshalled as the JSON-RPC result.
type RequestHandler func(params json.RawMessage) (any, error)

// NotificationHandler consumes an incoming peer notification.
type NotificationHandler func(params json.RawMessage)

// message is the wire form shared by requests, responses, and
// notifications. ID stays raw because peers may use numbers or strings.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Conn is a JSON-RPC 2.0 connection speaking one JSON object per
// newline-terminated line. Incoming requests and notifications are
// dispatched synchronously from the read loop, preserving the peer's
// message order; handlers must therefore not issue Calls of their own.
type Conn struct {
	writeMu sync.Mutex
	w       io.Writer

	mu       sync.Mutex
	nextID   int64
	pending  map[string]chan message
	requests map[string]RequestHandler
	notes    map[string]NotificationHandler
	closed   bool
	readErr  error

	done chan struct{}
}

// NewConn wraps a writer/reader pair (typically the peer process's
// stdin/stdout) and starts the read loop. Register handlers before the
// first Call — the peer only speaks after we do.
func NewConn(w io.Writer, r io.Reader) *Conn {
	c := &Conn{
		w:        w,
		pending:  map[string]chan message{},
		requests: map[string]RequestHandler{},
		notes:    map[string]NotificationHandler{},
		done:     make(chan struct{}),
	}
	go c.readLoop(r)
	return c
}

// OnRequest registers the handler for an incoming request method.
func (c *Conn) OnRequest(method string, h RequestHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests[method] = h
}

// OnNotification registers the handler for an incoming notification
// method.
func (c *Conn) OnNotification(method string, h NotificationHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notes[method] = h
}

// Call sends a request and blocks until the matching response, context
// cancellation, or connection teardown. A non-nil result receives the
// unmarshalled JSON-RPC result.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	if c.closed {
		err := c.teardownErrLocked()
		c.mu.Unlock()
		return err
	}
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	ch := make(chan message, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.send(message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: marshalParams(params)}); err != nil {
		return err
	}
	select {
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result == nil || len(resp.Result) == 0 {
			return nil
		}
		return json.Unmarshal(resp.Result, result)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.teardownErr()
	}
}

// Notify sends a notification (no id, no response).
func (c *Conn) Notify(method string, params any) error {
	return c.send(message{JSONRPC: "2.0", Method: method, Params: marshalParams(params)})
}

// Close tears the connection down and fails outstanding Calls. It does
// not close the underlying reader/writer; the caller owns those.
func (c *Conn) Close() {
	c.fail(fmt.Errorf("acp: connection closed"))
}

// Done is closed when the read loop has terminated (peer EOF, read
// error, or Close).
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns the reason the connection tore down, nil while live.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		return nil
	}
	return c.teardownErrLocked()
}

func (c *Conn) send(m message) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.w.Write(append(data, '\n'))
	return err
}

func (c *Conn) readLoop(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			continue // tolerate stray non-JSON output on the stream
		}
		switch {
		case m.Method != "" && m.ID != nil:
			c.serveRequest(m)
		case m.Method != "":
			c.serveNotification(m)
		case m.ID != nil:
			c.settle(m)
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.fail(fmt.Errorf("acp: peer connection lost: %w", err))
}

func (c *Conn) serveRequest(m message) {
	c.mu.Lock()
	h := c.requests[m.Method]
	c.mu.Unlock()
	resp := message{JSONRPC: "2.0", ID: m.ID}
	if h == nil {
		resp.Error = &RPCError{Code: codeMethodNotFound, Message: "method not found: " + m.Method}
	} else if result, err := h(m.Params); err != nil {
		resp.Error = &RPCError{Code: -32603, Message: err.Error()}
	} else {
		resp.Result = marshalParams(result)
	}
	_ = c.send(resp)
}

func (c *Conn) serveNotification(m message) {
	c.mu.Lock()
	h := c.notes[m.Method]
	c.mu.Unlock()
	if h != nil {
		h(m.Params)
	}
}

// settle routes a response to the Call waiting on its id.
func (c *Conn) settle(m message) {
	c.mu.Lock()
	ch := c.pending[normalizeID(m.ID)]
	c.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

// fail marks the connection closed with the given reason; the first
// reason wins.
func (c *Conn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.readErr = err
	close(c.done)
}

func (c *Conn) teardownErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.teardownErrLocked()
}

func (c *Conn) teardownErrLocked() error {
	if c.readErr != nil {
		return c.readErr
	}
	return fmt.Errorf("acp: connection closed")
}

// normalizeID maps a raw JSON id to the string key used in pending.
// Our outgoing ids are bare integers, so their raw form matches the
// strconv key exactly.
func normalizeID(raw json.RawMessage) string {
	return string(bytes.TrimSpace(raw))
}

func marshalParams(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return data
}
