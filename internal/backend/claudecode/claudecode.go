// Package claudecode implements the Claude Code CodergenBackend for
// Attractor pipelines (codergen-backends-spec §4). The backend wraps the
// `claude` CLI and supports two integration tiers:
//
//   - Tier 1: one-shot subprocess (`claude -p ... --output-format json`).
//     Returns the final JSON envelope; no event visibility beyond the
//     pipeline-level Outcome.
//   - Tier 2: structured stream + hook ingest
//     (`claude -p ... --output-format stream-json --verbose --settings <file>`).
//     The backend parses the line-delimited JSON stream into Attractor
//     events; the configured hookshim posts hook payloads to the engine's
//     ingest server.
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
	"time"

	"github.com/fabro/attractor/internal/backend"
	"github.com/fabro/attractor/internal/engine"
	"github.com/fabro/attractor/internal/ingest"
)

// Tier selects between tier 1 and tier 2 invocation modes.
type Tier int

const (
	Tier1 Tier = 1
	Tier2 Tier = 2
)

// Backend is the Claude Code CodergenBackend implementation.
type Backend struct {
	// ClaudeBin is the path to the `claude` executable. Empty defaults to
	// looking up the binary on PATH.
	ClaudeBin string
	// HookShimBin is the absolute path to the hookshim binary, used to
	// build the per-stage settings file. Empty disables hook integration
	// (tier 2 still works but emits no hook events).
	HookShimBin string
	// Tier selects the invocation mode. Defaults to Tier2.
	Tier Tier
	// Timeout caps a single Run invocation. Zero means no per-call timeout
	// beyond the node's own timeout attribute.
	Timeout time.Duration
	// IngestURL is the URL hookshims POST to. When non-empty, env vars
	// are propagated to the claude subprocess (ATTRACTOR_INGEST etc).
	IngestURL string
	// FallbackDir is the local file fallback for hook payloads when the
	// ingest URL is unreachable. Empty disables it.
	FallbackDir string
}

// Run satisfies backend.CodergenBackend.
func (b *Backend) Run(env engine.HandlerEnv, prompt string) (backend.Result, error) {
	tier := b.Tier
	if tier == 0 {
		tier = Tier2
	}
	switch tier {
	case Tier1:
		return b.runTier1(env, prompt)
	case Tier2:
		return b.runTier2(env, prompt)
	}
	return backend.Result{}, fmt.Errorf("claudecode: unknown tier %d", tier)
}

// runTier1 invokes claude with `--output-format json` and parses the
// final envelope.
func (b *Backend) runTier1(env engine.HandlerEnv, prompt string) (backend.Result, error) {
	cmd, cancel, err := b.prepareCmd(env, prompt, []string{"-p", prompt, "--output-format", "json"})
	if err != nil {
		return backend.Result{}, err
	}
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		return backend.Result{}, claudeErr(err, out)
	}
	envelope, perr := parseJSONEnvelope(out)
	if perr != nil {
		return backend.Result{}, fmt.Errorf("claudecode tier1: %w", perr)
	}
	return resultFromEnvelope(envelope), nil
}

// runTier2 invokes claude with stream-json + hook settings file.
func (b *Backend) runTier2(env engine.HandlerEnv, prompt string) (backend.Result, error) {
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

// parseJSONEnvelope decodes a JSON object. Tolerates concatenated JSON
// (the result envelope embedded in array brackets etc).
func parseJSONEnvelope(out []byte) (map[string]any, error) {
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, fmt.Errorf("empty claude output")
	}
	var env map[string]any
	if err := json.Unmarshal(out, &env); err == nil {
		return env, nil
	}
	// claude --output-format json sometimes wraps in an array.
	var arr []map[string]any
	if err := json.Unmarshal(out, &arr); err == nil && len(arr) > 0 {
		return arr[len(arr)-1], nil
	}
	return nil, fmt.Errorf("unrecognised claude envelope: %s", strings.TrimSpace(string(out)))
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

// _ keeps the ingest dependency live; servers wire in the IngestURL.
var _ = ingest.Server{}
