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

// linearIssue is the subset of an issue node mapped into an Item, shared
// by List and Get.
type linearIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	URL        string `json:"url"`
}

func (n linearIssue) item() Item {
	return Item{
		Ref:   engine.ItemRef{Source: "linear", Type: "issue", ExternalID: n.ID},
		Title: n.Title,
		URL:   n.URL,
		Vars: map[string]string{
			"url":        n.URL,
			"title":      n.Title,
			"identifier": n.Identifier,
		},
	}
}

type linearResp struct {
	Data struct {
		Viewer struct {
			AssignedIssues struct {
				Nodes []linearIssue `json:"nodes"`
			} `json:"assignedIssues"`
		} `json:"viewer"`
		Issue linearIssue `json:"issue"`
	} `json:"data"`
}

// List fetches the viewer's assigned issues. The Linear-side viewer
// scoping means Filter carries no extra query today.
func (l *Linear) List(ctx context.Context, _ Filter) ([]Item, error) {
	parsed, err := l.query(ctx, linearQuery)
	if err != nil {
		return nil, err
	}
	nodes := parsed.Data.Viewer.AssignedIssues.Nodes
	items := make([]Item, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, n.item())
	}
	return items, nil
}

// linearIssueQuery fetches a single issue by id (external-id form).
const linearIssueQuery = `query($id:String!){ issue(id:$id){ id identifier title url } }`

// Get resolves one issue by its ItemRef external id.
func (l *Linear) Get(ctx context.Context, ref engine.ItemRef) (Item, error) {
	parsed, err := l.query(ctx, linearIssueQuery, map[string]string{"id": ref.ExternalID})
	if err != nil {
		return Item{}, err
	}
	if parsed.Data.Issue.ID == "" {
		return Item{}, fmt.Errorf("linear: issue %q not found", ref.ExternalID)
	}
	return parsed.Data.Issue.item(), nil
}

// query POSTs a GraphQL request and decodes the shared linearResp. An
// optional variables map is included when non-nil.
func (l *Linear) query(ctx context.Context, gql string, vars ...map[string]string) (linearResp, error) {
	payload := map[string]any{"query": gql}
	if len(vars) > 0 {
		payload["variables"] = vars[0]
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearEndpoint, bytes.NewReader(body))
	if err != nil {
		return linearResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.apiKey)

	resp, err := l.doer.Do(req)
	if err != nil {
		return linearResp{}, fmt.Errorf("linear request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return linearResp{}, fmt.Errorf("linear status %d: %s", resp.StatusCode, msg)
	}
	var parsed linearResp
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return linearResp{}, fmt.Errorf("parse linear response: %w", err)
	}
	return parsed, nil
}
