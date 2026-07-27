package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// StreamEvents opens the run's SSE stream and returns a channel of
// parsed events. The channel is closed when the terminal event arrives
// (pipeline_completed / pipeline_failed), the stream EOFs, or ctx is
// cancelled. The initial connection is made synchronously so a bad
// request (e.g. unknown run) is reported as an error rather than a
// silently-closed channel.
func (c *Client) StreamEvents(ctx context.Context, id string) (<-chan Event, error) {
	return c.StreamEventsSince(ctx, id, 0)
}

// StreamEventsSince is StreamEvents resuming after a known sequence number:
// when since > 0 it requests GET /pipelines/{id}/events?since=<since> so the
// daemon replays only events with seq > since, letting a reconnecting client
// pick up where it left off without duplicating events (tui-spec T3).
func (c *Client) StreamEventsSince(ctx context.Context, id string, since int64) (<-chan Event, error) {
	path := "/pipelines/" + id + "/events"
	if since > 0 {
		path += "?since=" + strconv.FormatInt(since, 10)
	}
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, resp.Status)
	}

	out := make(chan Event)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var data strings.Builder
		for sc.Scan() {
			line := sc.Text()
			if line == "" { // frame boundary: dispatch what we accumulated
				if data.Len() == 0 {
					continue
				}
				raw := data.String()
				data.Reset()
				var ev Event
				if err := json.Unmarshal([]byte(raw), &ev); err != nil {
					continue
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
				if ev.Kind == "pipeline_completed" || ev.Kind == "pipeline_failed" {
					return
				}
				continue
			}
			if payload, ok := strings.CutPrefix(line, "data:"); ok {
				data.WriteString(strings.TrimPrefix(payload, " "))
			}
		}
	}()
	return out, nil
}
