// Hookshim: tiny binary launched by Claude Code's hook configuration.
// Reads the hook JSON payload on stdin, augments with Attractor correlation
// from env vars, POSTs to the ingest endpoint. Falls back to a local file
// if the POST fails so the agent is never blocked by transport problems.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "hookshim: missing hook name argument")
		os.Exit(2)
	}
	hookName := os.Args[1]

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hookshim: read stdin: %v\n", err)
		os.Exit(0) // never block the agent
	}

	var payload map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &payload); err != nil {
			payload = map[string]any{"raw": string(raw)}
		}
	} else {
		payload = map[string]any{}
	}
	payload["hook_name"] = hookName
	payload["run_id"] = os.Getenv("ATTRACTOR_RUN_ID")
	payload["stage_id"] = os.Getenv("ATTRACTOR_STAGE_ID")
	payload["ts"] = time.Now().UTC().Format(time.RFC3339Nano)

	body, _ := json.Marshal(payload)

	ingest := os.Getenv("ATTRACTOR_INGEST")
	if ingest != "" {
		client := &http.Client{Timeout: 2 * time.Second}
		req, _ := http.NewRequest("POST", ingest, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			return
		}
	}

	fallback := os.Getenv("ATTRACTOR_FALLBACK_DIR")
	if fallback == "" {
		return
	}
	_ = os.MkdirAll(fallback, 0o755)
	f, err := os.OpenFile(filepath.Join(fallback, "fallback.jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(body)
	f.Write([]byte("\n"))
}
