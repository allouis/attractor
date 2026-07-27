// Package client is a typed Go client for the Attractor daemon's HTTP
// API (tui-spec T1). It is the single contract every non-web UI builds
// on: the TUI drives runs through these methods instead of ad-hoc
// map[string]any. The package is deliberately stdlib-only so it can
// land before the TUI's external dependencies.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client talks to an Attractor daemon at BaseURL. HTTP defaults to
// http.DefaultClient when nil. Token, when set, is sent as a bearer
// token so the client works against an --auth-token daemon.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Token   string
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// newRequest builds a request against BaseURL, attaching the bearer
// token and, when body is non-nil, a JSON content type.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// doJSON sends req and decodes a 2xx JSON body into out (skipped when
// out is nil). Non-2xx responses become errors carrying the status and
// a snippet of the body.
func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// getJSON issues a GET and decodes the JSON response into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return c.doJSON(req, out)
}

// postJSON marshals body (nil sends no body) and decodes the response
// into out.
func (c *Client) postJSON(ctx context.Context, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, r)
	if err != nil {
		return err
	}
	return c.doJSON(req, out)
}

// ListRuns returns the daemon's run summaries, newest first.
func (c *Client) ListRuns(ctx context.Context) ([]RunSummary, error) {
	var body struct {
		Pipelines []RunSummary `json:"pipelines"`
	}
	if err := c.getJSON(ctx, "/pipelines", &body); err != nil {
		return nil, err
	}
	return body.Pipelines, nil
}

// GetRun returns a single run's summary.
func (c *Client) GetRun(ctx context.Context, id string) (RunSummary, error) {
	var run RunSummary
	err := c.getJSON(ctx, "/pipelines/"+id, &run)
	return run, err
}

// GetStage returns a stage's inline output (prompt, response, tool calls,
// and self-reported status) for run id and node.
func (c *Client) GetStage(ctx context.Context, id, node string) (StageDetail, error) {
	var detail StageDetail
	err := c.getJSON(ctx, "/pipelines/"+id+"/stages/"+node, &detail)
	return detail, err
}

// Submit enqueues a new run and returns its id.
func (c *Client) Submit(ctx context.Context, req SubmitRequest) (string, error) {
	var body struct {
		ID string `json:"id"`
	}
	if err := c.postJSON(ctx, "/pipelines", req, &body); err != nil {
		return "", err
	}
	return body.ID, nil
}

// Cancel requests cancellation of a run.
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.postJSON(ctx, "/pipelines/"+id+"/cancel", nil, nil)
}

// ListQuestions returns a run's open human gates.
func (c *Client) ListQuestions(ctx context.Context, id string) ([]Question, error) {
	var body struct {
		Questions []Question `json:"questions"`
	}
	if err := c.getJSON(ctx, "/pipelines/"+id+"/questions", &body); err != nil {
		return nil, err
	}
	return body.Questions, nil
}

// Answer selects an option for a pending question, resuming the run.
func (c *Client) Answer(ctx context.Context, id, qid, optionID string) error {
	return c.postJSON(ctx, "/pipelines/"+id+"/questions/"+qid+"/answer", map[string]string{"key": optionID}, nil)
}
