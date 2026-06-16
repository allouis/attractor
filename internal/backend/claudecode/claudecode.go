// Package claudecode implements the Claude Code CodergenBackend for
// Attractor pipelines (codergen-backends-spec §4). The backend wraps the
// `claude` CLI in its structured stream-JSON mode, parses events as
// they arrive, and forwards lifecycle hooks to the engine's ingest
// server for visibility and tool_hooks dispatch.
//
// The package never speaks to the Anthropic API directly — provider
// abstraction is delegated to the `claude` binary itself, matching the
// "wrap an existing agent CLI" strategy from spec §1.2.
package claudecode

import (
	"bufio"
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

	"github.com/fabro/attractor/internal/backend"
	"github.com/fabro/attractor/internal/engine"
)

// Backend is the Claude Code CodergenBackend implementation.
type Backend struct {
	// ClaudeBin is the path to the `claude` executable. Empty defaults to
	// looking up the binary on PATH.
	ClaudeBin string
	// HookShimBin is the absolute path to the hookshim binary, used to
	// build the per-stage settings file. Empty disables hook integration
	// (the backend still works but emits no hook events).
	HookShimBin string
	// Timeout caps a single Run invocation. Zero means no per-call timeout
	// beyond the node's own timeout attribute.
	Timeout time.Duration
	// IngestURL is the URL hookshims POST to. When non-empty, env vars
	// are propagated to the claude subprocess (ATTRACTOR_INGEST etc).
	IngestURL string
	// FallbackDir is the local file fallback for hook payloads when the
	// ingest URL is unreachable. Empty disables it.
	FallbackDir string

	// sessions tracks the claude session id observed for each thread_id
	// resolved by the engine for full-fidelity stages (spec §5.4).
	// Subsequent stages sharing a thread_id resume via `claude --resume`
	// so the agent literally continues the prior conversation.
	sessions sync.Map // map[string]string
}

// Run satisfies backend.CodergenBackend by invoking claude with
// stream-json output and a generated hook settings file.
func (b *Backend) Run(env engine.HandlerEnv, prompt string) (backend.Result, error) {
	settingsPath := ""
	if b.HookShimBin != "" {
		path, err := b.writeSettingsFile(env)
		if err != nil {
			return backend.Result{}, err
		}
		defer os.Remove(path)
		settingsPath = path
	}
	args := []string{"-p", prompt, "--output-format", "stream-json", "--verbose"}
	if settingsPath != "" {
		args = append(args, "--settings", settingsPath)
	}
	// Full-fidelity stages reuse the prior claude session via --resume
	// so the agent continues the same conversation across stages
	// (spec §5.4 session reuse). Fresh sessions (compact/summary/
	// truncate) deliberately don't pass --resume.
	resuming := false
	if env.Fidelity == engine.FidelityFull && env.ThreadID != "" {
		if sid := b.sessionFor(env.ThreadID); sid != "" {
			args = append([]string{"--resume", sid}, args...)
			resuming = true
		}
	}
	_ = resuming // used below to decide whether to remember
	cmd, cancel, err := b.prepareCmd(env, prompt, args)
	if err != nil {
		return backend.Result{}, err
	}
	defer cancel()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return backend.Result{}, err
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return backend.Result{}, err
	}
	var finalEnvelope map[string]any
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		kind, _ := rec["type"].(string)
		switch kind {
		case "system":
			if sub, _ := rec["subtype"].(string); sub == "init" {
				if sid, _ := rec["session_id"].(string); sid != "" && env.Fidelity == engine.FidelityFull {
					b.rememberSession(env.ThreadID, sid)
				}
			}
		case "assistant":
			if env.Emit != nil {
				text := assistantText(rec)
				if text != "" {
					env.Emit(engine.Event{
						Kind: engine.EventStageProgress, NodeID: env.Node.ID,
						Message: text, Detail: map[string]string{"kind": "assistant_delta"},
					})
				}
			}
		case "result":
			if sid, _ := rec["session_id"].(string); sid != "" && env.Fidelity == engine.FidelityFull {
				b.rememberSession(env.ThreadID, sid)
			}
			finalEnvelope = rec
		}
	}
	if err := scanner.Err(); err != nil {
		return backend.Result{}, fmt.Errorf("claudecode tier2: read stream: %w", err)
	}
	if err := cmd.Wait(); err != nil {
		return backend.Result{}, claudeErr(err, stderr.Bytes())
	}
	if finalEnvelope == nil {
		return backend.Result{ResponseText: ""}, nil
	}
	return resultFromEnvelope(finalEnvelope), nil
}

// sessionFor returns the recorded claude session id for the thread, or
// empty string when none. Callers should also check that the engine
// resolved full fidelity before using the result.
func (b *Backend) sessionFor(threadID string) string {
	if threadID == "" {
		return ""
	}
	v, ok := b.sessions.Load(threadID)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// rememberSession stores the claude session id captured from the
// stream-json output so subsequent same-thread stages can resume it.
func (b *Backend) rememberSession(threadID, sessionID string) {
	if threadID == "" || sessionID == "" {
		return
	}
	b.sessions.Store(threadID, sessionID)
}

// prepareCmd builds an *exec.Cmd with the correlation env vars set and
// optional per-run timeout cancellation wired in.
func (b *Backend) prepareCmd(env engine.HandlerEnv, prompt string, args []string) (*exec.Cmd, context.CancelFunc, error) {
	bin := b.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, func() {}, fmt.Errorf("claudecode: %w", err)
	}
	ctx := context.Background()
	cancel := func() {}
	if b.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, b.Timeout)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(),
		"ATTRACTOR_RUN_ID="+env.RunID,
		"ATTRACTOR_STAGE_ID="+env.Node.ID,
		"ATTRACTOR_STAGE_DIR="+filepath.Join(env.LogsRoot, env.Node.ID),
		"ATTRACTOR_INGEST="+b.IngestURL,
		"ATTRACTOR_FALLBACK_DIR="+b.FallbackDir,
	)
	return cmd, cancel, nil
}

// writeSettingsFile materialises a per-stage Claude Code settings file
// that wires every hook to the hookshim binary. The file lives under
// the stage's log directory so it can be inspected post-mortem.
func (b *Backend) writeSettingsFile(env engine.HandlerEnv) (string, error) {
	hooks := []struct {
		Name string
		Arg  string
	}{
		{"SessionStart", "session_start"},
		{"UserPromptSubmit", "user_prompt"},
		{"PreToolUse", "pre_tool"},
		{"PostToolUse", "post_tool"},
		{"Stop", "stop"},
		{"SubagentStop", "subagent_stop"},
		{"Notification", "notification"},
		{"PreCompact", "pre_compact"},
	}
	cfg := map[string]any{
		"hooks": map[string]any{},
	}
	for _, h := range hooks {
		entry := map[string]any{
			"hooks": []any{
				map[string]any{
					"type":    "command",
					"command": fmt.Sprintf("%s %s", b.HookShimBin, h.Arg),
				},
			},
		}
		if h.Name == "PreToolUse" || h.Name == "PostToolUse" {
			entry["matcher"] = "*"
		}
		cfg["hooks"].(map[string]any)[h.Name] = []any{entry}
	}
	dir := filepath.Join(env.LogsRoot, env.Node.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "claude.settings.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func resultFromEnvelope(envelope map[string]any) backend.Result {
	res := backend.Result{}
	if v, ok := envelope["result"].(string); ok {
		res.ResponseText = v
	}
	isErr, _ := envelope["is_error"].(bool)
	if isErr {
		o := engine.Outcome{
			Status:        engine.StatusFail,
			FailureReason: stringField(envelope, "error", "claude reported is_error"),
		}
		res.Outcome = &o
	}
	return res
}

func stringField(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func assistantText(rec map[string]any) string {
	msg, ok := rec["message"].(map[string]any)
	if !ok {
		return ""
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return ""
	}
	var out strings.Builder
	for _, item := range content {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := obj["text"].(string); ok {
			out.WriteString(t)
		}
	}
	return out.String()
}

func claudeErr(err error, stderr []byte) error {
	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		return fmt.Errorf("claude: %w", err)
	}
	return fmt.Errorf("claude: %w: %s", err, msg)
}

// AvailableAuth reports whether the host has either ANTHROPIC_API_KEY in
// the environment or a Claude OAuth credentials file. Engine startup
// uses it for the codergen_subscription_auth lint diagnostic.
func AvailableAuth() bool {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return true
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths := []string{
			filepath.Join(home, ".claude", "credentials.json"),
			filepath.Join(home, ".claude", ".credentials.json"),
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return true
			}
		}
	}
	return false
}

