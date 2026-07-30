// Package report is the child-side of attractor's phone-home reporting.
// A launched run (`attractor run --report-to`) uses it to stream engine
// events, upload artifacts, and poll for control to the daemon that
// started it, over the same HTTP surface for local-subprocess, VM, and
// remote placements.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/allouis/attractor/internal/engine"
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

// Forward drains an engine event channel, reporting each event. It
// returns when the channel closes. Per-event errors are ignored so a
// transient daemon hiccup never stalls the run (the durable record is
// the daemon's own events.jsonl once delivered).
func (c *Client) Forward(ch <-chan engine.Event) {
	for ev := range ch {
		_ = c.Event(ev)
	}
}
