package source

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/fabro/attractor/internal/engine"
)

// linearEndpoint is Linear's GraphQL API.
const linearEndpoint = "https://api.linear.app/graphql"

// linearQuery fetches the viewer's assigned issues — Linear scopes
// "assigned to me" server-side via the authenticated viewer.
const linearQuery = `{ viewer { assignedIssues(first: 50) { nodes { id identifier title url } } } }`

// httpDoer is the subset of *http.Client the Linear source needs;
// injectable so tests replay canned GraphQL responses.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Linear lists issues assigned to the viewer via Linear's GraphQL API,
// authenticated by a config API key (items-spec §8).
type Linear struct {
	apiKey string
	doer   httpDoer
}

// NewLinear returns a Linear source authenticated with apiKey.
func NewLinear(apiKey string) *Linear {
	return &Linear{apiKey: apiKey, doer: http.DefaultClient}
}

type linearResp struct {
	Data struct {
		Viewer struct {
			AssignedIssues struct {
				Nodes []struct {
					ID         string `json:"id"`
					Identifier string `json:"identifier"`
					Title      string `json:"title"`
					URL        string `json:"url"`
				} `json:"nodes"`
			} `json:"assignedIssues"`
		} `json:"viewer"`
	} `json:"data"`
}

// List fetches the viewer's assigned issues. The Linear-side viewer
// scoping means Filter carries no extra query today.
func (l *Linear) List(ctx context.Context, _ Filter) ([]Item, error) {
	body, _ := json.Marshal(map[string]string{"query": linearQuery})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.apiKey)

	resp, err := l.doer.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("linear status %d: %s", resp.StatusCode, msg)
	}

	var parsed linearResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("parse linear response: %w", err)
	}
	nodes := parsed.Data.Viewer.AssignedIssues.Nodes
	items := make([]Item, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, Item{
			Ref:   engine.ItemRef{Source: "linear", Type: "issue", ExternalID: n.ID},
			Title: n.Title,
			URL:   n.URL,
			Vars: map[string]string{
				"url":        n.URL,
				"title":      n.Title,
				"identifier": n.Identifier,
			},
		})
	}
	return items, nil
}
