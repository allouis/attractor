// Package report is the child-side of attractor's phone-home reporting.
// A launched run (`attractor run --report-to`) uses it to stream engine
// events, upload artifacts, and poll for control to the daemon that
// started it, over the same HTTP surface for local-subprocess, VM, and
// remote placements.
package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/allouis/attractor/internal/engine"
	"github.com/allouis/attractor/internal/version"
)

// Client reports one run's activity to the daemon.
type Client struct {
	base  string
	runID string
	token string
	hc    *http.Client
}

// New returns a reporting client for runID authenticating with token
// against the daemon at base (e.g. http://127.0.0.1:7681).
func New(base, runID, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		runID: runID,
		token: token,
		hc:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) url(suffix string) string {
	return fmt.Sprintf("%s/pipelines/%s%s", c.base, c.runID, suffix)
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	// Advertise the child's build so the daemon can warn on skew (e.g. a
	// VM image baking a stale attractor).
	req.Header.Set(version.RevisionHeader, version.Get())
	return c.hc.Do(req)
}

// Event reports one engine event to the daemon.
func (c *Client) Event(ev engine.Event) error {
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.url("/events"), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("report event: %s", resp.Status)
	}
	return nil
}

// UploadArtifact stores one file's bytes under the run's logs root at the
// given relative path (forward-slash separated).
func (c *Client) UploadArtifact(rel string, data []byte) error {
	req, err := http.NewRequest(http.MethodPost, c.url("/artifacts/"+rel), bytes.NewReader(data))
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("upload %s: %s", rel, resp.Status)
	}
	return nil
}

// UploadDir walks root and uploads every regular file, keying each by its
// path relative to root (forward slashes). skip, when non-nil, drops a
// file whose rel path it returns true for — used to omit files the daemon
// owns itself (events.jsonl, manifest.json, source.dot).
func (c *Client) UploadDir(root string, skip func(rel string) bool) error {
	return c.uploadTree(root, root, skip)
}

// UploadStageDir uploads one stage's directory (root/node and everything
// beneath it — including nested child-pipeline dirs) incrementally, keying
// each file by its path relative to root: the same key the terminal
// UploadDir sweep uses, so a completed stage's files reach the daemon while
// the run is still live. A stage dir that does not exist yet (a stage that
// wrote nothing) is not an error — the terminal sweep is the catch-all.
// skip is applied with the run-root-relative rel path, exactly as UploadDir.
func (c *Client) UploadStageDir(root, node string, skip func(rel string) bool) error {
	sub := filepath.Join(root, node)
	if _, err := os.Stat(sub); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return c.uploadTree(root, sub, skip)
}

// uploadTree walks from (a directory within root) and uploads every regular
// file under it, keying each by its path relative to root.
func (c *Client) uploadTree(root, from string, skip func(rel string) bool) error {
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if skip != nil && skip(rel) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return c.UploadArtifact(rel, data)
	})
}

// Forward drains an engine event channel, reporting each event. It
// returns when the channel closes. Per-event errors are ignored so a
// transient daemon hiccup never stalls the run (the durable record is
// the daemon's own events.jsonl once delivered).
func (c *Client) Forward(ch <-chan engine.Event) {
	for ev := range ch {
		_ = c.Event(ev)
	}
}
