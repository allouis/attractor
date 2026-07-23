package acp

import (
	"context"
	"encoding/json"
)

// StopReason is the agent's reason for ending a prompt turn.
type StopReason string

const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopMaxTurnRequests StopReason = "max_turn_requests"
	StopRefusal         StopReason = "refusal"
	StopCancelled       StopReason = "cancelled"
)

// InitializeResult is the subset of the agent's initialize response
// attractor cares about.
type InitializeResult struct {
	ProtocolVersion     int
	SupportsLoadSession bool
}

// SessionUpdate is one session/update notification, flattened for
// consumers. Raw always holds the full update object for logging.
type SessionUpdate struct {
	SessionID string
	// Kind is the sessionUpdate discriminator: agent_message_chunk,
	// agent_thought_chunk, tool_call, tool_call_update, plan, ...
	Kind string
	// Text is the chunk text for *_chunk updates with text content.
	Text string
	// ToolCall is set for tool_call / tool_call_update kinds.
	ToolCall *ToolCallUpdate
	// Usage is set when the update carries a token-usage payload (the
	// `usage` sub-object, present on the "usage" sessionUpdate variant
	// or piggybacked on another update). Nil when absent.
	Usage *TokenUsage
	Raw   json.RawMessage
}

// TokenUsage carries the agent-reported token counts for a turn.
type TokenUsage struct {
	InputTokens  int
	OutputTokens int
}

// ToolCallUpdate carries the common fields of tool_call and
// tool_call_update session updates.
type ToolCallUpdate struct {
	ID     string
	Title  string
	Kind   string
	Status string
}

// PermissionOption is one choice offered by a session/request_permission.
type PermissionOption struct {
	ID   string
	Name string
	Kind string
}

// PermissionRequest is an agent request for permission to run a tool.
type PermissionRequest struct {
	SessionID string
	ToolTitle string
	Options   []PermissionOption
}

// ClientConfig carries the observer callbacks a Client dispatches to.
// Both are invoked synchronously from the connection's read loop.
type ClientConfig struct {
	// OnUpdate receives every session/update notification.
	OnUpdate func(SessionUpdate)
	// OnPermission observes each permission request together with the
	// auto-selected option id ("" when the request was cancelled).
	OnPermission func(req PermissionRequest, chosenOptionID string)
}

// Client implements the ACP client role on top of a Conn: lifecycle
// requests out, session updates and permission requests in. Attractor
// runs agents headless, so permission requests are auto-granted
// (allow_always over allow_once over any non-reject option); the agent
// shares the working tree and does its own file I/O, so no fs or
// terminal capabilities are advertised.
type Client struct {
	conn *Conn
	cfg  ClientConfig
}

// NewClient wires the session/update and session/request_permission
// handlers onto conn and returns the client.
func NewClient(conn *Conn, cfg ClientConfig) *Client {
	c := &Client{conn: conn, cfg: cfg}
	conn.OnNotification("session/update", c.handleUpdate)
	conn.OnRequest("session/request_permission", c.handlePermission)
	return c
}

// Initialize performs the ACP handshake, advertising protocol v1 and
// no client capabilities.
func (c *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	params := map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"fs": map[string]any{
				"readTextFile":  false,
				"writeTextFile": false,
			},
			"terminal": false,
		},
	}
	var res struct {
		ProtocolVersion   int `json:"protocolVersion"`
		AgentCapabilities struct {
			LoadSession bool `json:"loadSession"`
		} `json:"agentCapabilities"`
	}
	if err := c.conn.Call(ctx, "initialize", params, &res); err != nil {
		return InitializeResult{}, err
	}
	return InitializeResult{
		ProtocolVersion:     res.ProtocolVersion,
		SupportsLoadSession: res.AgentCapabilities.LoadSession,
	}, nil
}

// NewSession creates a fresh agent session rooted at cwd.
func (c *Client) NewSession(ctx context.Context, cwd string) (string, error) {
	params := map[string]any{"cwd": cwd, "mcpServers": []any{}}
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.conn.Call(ctx, "session/new", params, &res); err != nil {
		return "", err
	}
	return res.SessionID, nil
}

// LoadSession resumes a previously created session. Only valid when
// the agent advertised loadSession support.
func (c *Client) LoadSession(ctx context.Context, sessionID, cwd string) error {
	params := map[string]any{"sessionId": sessionID, "cwd": cwd, "mcpServers": []any{}}
	return c.conn.Call(ctx, "session/load", params, nil)
}

// Prompt sends one user turn and blocks until the agent finishes it.
// Session updates stream to OnUpdate while this call is in flight.
func (c *Client) Prompt(ctx context.Context, sessionID, text string) (StopReason, error) {
	params := map[string]any{
		"sessionId": sessionID,
		"prompt": []any{
			map[string]any{"type": "text", "text": text},
		},
	}
	var res struct {
		StopReason string `json:"stopReason"`
	}
	if err := c.conn.Call(ctx, "session/prompt", params, &res); err != nil {
		return "", err
	}
	return StopReason(res.StopReason), nil
}

// Cancel asks the agent to stop the session's current turn.
func (c *Client) Cancel(sessionID string) error {
	return c.conn.Notify("session/cancel", map[string]any{"sessionId": sessionID})
}

func (c *Client) handleUpdate(params json.RawMessage) {
	if c.cfg.OnUpdate == nil {
		return
	}
	var note struct {
		SessionID string          `json:"sessionId"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &note); err != nil {
		return
	}
	var body struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Text string `json:"text"`
		} `json:"content"`
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		Usage      *struct {
			InputTokens  int `json:"inputTokens"`
			OutputTokens int `json:"outputTokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(note.Update, &body); err != nil {
		return
	}
	u := SessionUpdate{
		SessionID: note.SessionID,
		Kind:      body.SessionUpdate,
		Text:      body.Content.Text,
		Raw:       note.Update,
	}
	if body.Usage != nil {
		u.Usage = &TokenUsage{
			InputTokens:  body.Usage.InputTokens,
			OutputTokens: body.Usage.OutputTokens,
		}
	}
	if body.ToolCallID != "" {
		u.ToolCall = &ToolCallUpdate{
			ID:     body.ToolCallID,
			Title:  body.Title,
			Kind:   body.Kind,
			Status: body.Status,
		}
	}
	c.cfg.OnUpdate(u)
}

func (c *Client) handlePermission(params json.RawMessage) (any, error) {
	var req struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			Title string `json:"title"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, err
	}
	pr := PermissionRequest{SessionID: req.SessionID, ToolTitle: req.ToolCall.Title}
	for _, o := range req.Options {
		pr.Options = append(pr.Options, PermissionOption{ID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	chosen := selectPermissionOption(pr.Options)
	if c.cfg.OnPermission != nil {
		c.cfg.OnPermission(pr, chosen)
	}
	if chosen == "" {
		return map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, nil
	}
	return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": chosen}}, nil
}

// selectPermissionOption auto-grants: allow_always beats allow_once
// beats any other non-reject option; nothing grantable returns "".
func selectPermissionOption(options []PermissionOption) string {
	byKind := func(kind string) string {
		for _, o := range options {
			if o.Kind == kind {
				return o.ID
			}
		}
		return ""
	}
	if id := byKind("allow_always"); id != "" {
		return id
	}
	if id := byKind("allow_once"); id != "" {
		return id
	}
	for _, o := range options {
		if o.Kind != "reject_once" && o.Kind != "reject_always" {
			return o.ID
		}
	}
	return ""
}
