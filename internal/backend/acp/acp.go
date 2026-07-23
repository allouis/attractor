// Package acp implements the ACP (Agent Client Protocol) CodergenBackend.
// It spawns an ACP agent process per stage (e.g. claude-agent-acp),
// drives one prompt turn over newline-delimited JSON-RPC on the
// process's stdio, and maps streamed session updates onto Attractor
// progress events. Unlike the claudecode backend there is no hookshim:
// tool visibility comes from the protocol's tool_call updates, which
// are persisted under the stage log directory.
package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fabro/attractor/internal/acp"
	"github.com/fabro/attractor/internal/backend"
	"github.com/fabro/attractor/internal/engine"
)

// killGrace is how long the backend waits for the agent process to
// exit after stdin closes before killing it.
const killGrace = 2 * time.Second

// Backend is the ACP CodergenBackend implementation. There is no
// default agent command: one must come from the node's `acp_command`
// attribute, the graph-level `acp_command` attribute, or Command (the
// CLI-level fallback), in that order of precedence.
type Backend struct {
	// Command is the fallback agent command line when neither the node
	// nor the graph sets `acp_command`. Leading NAME=value tokens become
	// environment for the agent process (e.g. ANTHROPIC_MODEL=... to
	// pick the model).
	Command string
	// ModelEnv, when set, is the environment variable the node's
	// `llm_model` attribute is injected through (the provider config's
	// model_env). This is per-node, so one Backend cached per provider
	// can still serve nodes selecting different models. Injection is
	// orthogonal to command precedence: it applies whichever command
	// (node / graph / Command) resolves.
	ModelEnv string
	// Timeout caps a single Run invocation. Zero means no timeout.
	Timeout time.Duration

	// sessions maps engine thread ids to ACP session ids for session
	// reuse under full fidelity.
	sessions sync.Map // map[string]string
}

// Run satisfies backend.CodergenBackend: spawn the agent, run one
// prompt turn, and translate the stop reason into a Result.
func (b *Backend) Run(env engine.HandlerEnv, prompt string) (backend.Result, error) {
	raw := b.resolveCommand(env)
	if raw == "" {
		return backend.Result{}, fmt.Errorf("acp: no agent command configured — set the node or graph `acp_command` attribute or the --acp-cmd flag")
	}
	prog, args, extraEnv, err := parseCommandLine(raw)
	if err != nil {
		return backend.Result{}, err
	}

	// Node `timeout` attribute wins over the backend-level default,
	// matching the tool handler's convention.
	timeout := b.Timeout
	if env.Node != nil {
		if d, ok := env.Node.Duration("timeout"); ok && d > 0 {
			timeout = d
		}
	}
	ctx := context.Background()
	cancel := func() {}
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, prog, args...)
	if env.Cwd != "" {
		cmd.Dir = env.Cwd
	}
	cmd.Env = append(os.Environ(), extraEnv...)
	if b.ModelEnv != "" && env.Node != nil {
		if model := strings.TrimSpace(env.Node.Attrs["llm_model"]); model != "" {
			cmd.Env = append(cmd.Env, b.ModelEnv+"="+model)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return backend.Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return backend.Result{}, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return backend.Result{}, fmt.Errorf("acp: start agent %q: %w", prog, err)
	}

	conn := acp.NewConn(stdin, stdout)
	defer func() {
		conn.Close()
		_ = stdin.Close()
		waitOrKill(cmd)
	}()

	turn := &turnState{env: env}
	client := acp.NewClient(conn, acp.ClientConfig{
		OnUpdate:     turn.onUpdate,
		OnPermission: turn.onPermission,
	})

	stop, err := b.runTurn(ctx, client, env, prompt)
	if err != nil {
		return backend.Result{}, agentErr(err, stderr.Bytes())
	}
	turn.emitUsage()
	return resultFromStop(stop, turn.text()), nil
}

// runTurn performs the handshake and the prompt round-trip. Under
// full fidelity, stages sharing a thread resume the recorded agent
// session via session/load (spec §5.4 session reuse) when the agent
// advertises loadSession; anything else starts fresh.
func (b *Backend) runTurn(ctx context.Context, client *acp.Client, env engine.HandlerEnv, prompt string) (acp.StopReason, error) {
	init, err := client.Initialize(ctx)
	if err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	cwd := env.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	reusable := env.Fidelity == engine.FidelityFull && env.ThreadID != ""
	sessionID := ""
	if reusable && init.SupportsLoadSession {
		if prev, ok := b.sessions.Load(env.ThreadID); ok {
			sid := prev.(string)
			if err := client.LoadSession(ctx, sid, cwd); err == nil {
				sessionID = sid
			}
			// A failed load falls through to a fresh session: the agent
			// may have expired or never persisted the conversation.
		}
	}
	if sessionID == "" {
		sessionID, err = client.NewSession(ctx, cwd)
		if err != nil {
			return "", fmt.Errorf("session/new: %w", err)
		}
	}
	if reusable {
		b.sessions.Store(env.ThreadID, sessionID)
	}
	stop, err := client.Prompt(ctx, sessionID, prompt)
	if err != nil {
		return "", fmt.Errorf("session/prompt: %w", err)
	}
	return stop, nil
}

// resolveCommand picks the agent command line: node attribute first,
// then graph attribute, then the CLI-level fallback.
func (b *Backend) resolveCommand(env engine.HandlerEnv) string {
	if env.Node != nil {
		if cmd := strings.TrimSpace(env.Node.Attrs["acp_command"]); cmd != "" {
			return cmd
		}
	}
	if env.Graph != nil {
		if cmd := strings.TrimSpace(env.Graph.Attrs["acp_command"]); cmd != "" {
			return cmd
		}
	}
	return strings.TrimSpace(b.Command)
}

// turnState accumulates one turn's streamed output: response text,
// progress events, and persisted tool-call payloads.
type turnState struct {
	env engine.HandlerEnv

	mu        sync.Mutex
	buf       strings.Builder
	toolCalls int
	usage     engine.Usage
}

func (t *turnState) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

func (t *turnState) onUpdate(u acp.SessionUpdate) {
	if u.Usage != nil {
		t.mu.Lock()
		t.usage.Add(engine.Usage{
			InputTokens:  u.Usage.InputTokens,
			OutputTokens: u.Usage.OutputTokens,
		})
		t.mu.Unlock()
	}
	switch u.Kind {
	case "agent_message_chunk":
		t.mu.Lock()
		t.buf.WriteString(u.Text)
		t.mu.Unlock()
		t.emit(engine.Event{
			Kind: engine.EventStageProgress, NodeID: t.env.Node.ID,
			Message: u.Text, Detail: map[string]string{"kind": "assistant_delta"},
		})
	case "tool_call", "tool_call_update":
		if u.ToolCall == nil {
			return
		}
		t.persistToolCall(u)
		msg := u.ToolCall.Title
		if msg == "" {
			msg = u.ToolCall.Status
		}
		t.emit(engine.Event{
			Kind: engine.EventStageProgress, NodeID: t.env.Node.ID,
			Message: msg, Detail: map[string]string{
				"kind":         "tool_call",
				"tool_call_id": u.ToolCall.ID,
				"status":       u.ToolCall.Status,
			},
		})
	}
}

func (t *turnState) onPermission(req acp.PermissionRequest, chosen string) {
	t.emit(engine.Event{
		Kind: engine.EventStageProgress, NodeID: t.env.Node.ID,
		Message: "permission: " + req.ToolTitle,
		Detail:  map[string]string{"kind": "permission", "chosen_option": chosen},
	})
}

// emitUsage emits the per-stage token total once the turn ends, so the
// engine can roll it up per run (service-spec §6). No usage reported
// (zero total) emits nothing.
func (t *turnState) emitUsage() {
	t.mu.Lock()
	total := t.usage
	t.mu.Unlock()
	if total.InputTokens == 0 && total.OutputTokens == 0 {
		return
	}
	u := total
	t.emit(engine.Event{
		Kind:   engine.EventUsage,
		NodeID: t.env.Node.ID,
		Usage:  &u,
	})
}

func (t *turnState) emit(ev engine.Event) {
	if t.env.Emit != nil {
		t.env.Emit(ev)
	}
}

// persistToolCall writes the raw update payload under the stage's
// tool_calls directory, mirroring the hookshim ingest layout.
func (t *turnState) persistToolCall(u acp.SessionUpdate) {
	t.mu.Lock()
	t.toolCalls++
	n := t.toolCalls
	t.mu.Unlock()
	dir := filepath.Join(t.env.LogsRoot, t.env.Node.ID, "tool_calls")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(json.RawMessage(u.Raw), "", "  ")
	if err != nil {
		data = u.Raw
	}
	path := filepath.Join(dir, fmt.Sprintf("%03d-%s.json", n, u.Kind))
	_ = os.WriteFile(path, data, 0o644)
}

// resultFromStop maps the agent's stop reason onto a backend Result.
func resultFromStop(stop acp.StopReason, text string) backend.Result {
	switch stop {
	case acp.StopEndTurn:
		return backend.Result{ResponseText: text}
	case acp.StopRefusal:
		return backend.Result{
			ResponseText: text,
			Outcome: &engine.Outcome{
				Status:        engine.StatusFail,
				FailureReason: "acp: agent refused the prompt",
			},
		}
	default:
		return backend.Result{
			ResponseText: text,
			Outcome: &engine.Outcome{
				Status:        engine.StatusFail,
				FailureReason: fmt.Sprintf("acp: agent stopped early: %s", stop),
			},
		}
	}
}

// waitOrKill reaps the agent process, killing it if it outlives the
// post-stdin-close grace period.
func waitOrKill(cmd *exec.Cmd) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(killGrace):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
}

// agentErr decorates a protocol error with the agent's stderr tail so
// startup failures (bad auth, missing binary deps) surface usefully.
func agentErr(err error, stderr []byte) error {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		return fmt.Errorf("acp: %w", err)
	}
	const maxTail = 2048
	if len(msg) > maxTail {
		msg = "…" + msg[len(msg)-maxTail:]
	}
	return fmt.Errorf("acp: %w: agent stderr: %s", err, msg)
}
