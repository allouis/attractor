package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/allouis/attractor/internal/items"
)

// GitHub lists open PRs and issues (mine: authored or assigned) via the
// machine-authed `gh` CLI (items-spec §8: the first source, PR → review,
// issue → implement). The runner is injectable so tests feed canned JSON
// without touching the network.
type GitHub struct {
	run func(ctx context.Context, args ...string) ([]byte, error)
}

// NewGitHub returns a GitHub source backed by the real `gh` binary.
func NewGitHub() *GitHub {
	return &GitHub{run: ghExec}
}

// ghExec shells out to `gh`, returning combined stdout. A non-zero exit
// surfaces gh's stderr in the error.
func ghExec(ctx context.Context, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %v: %s", args, ee.Stderr)
		}
		return nil, fmt.Errorf("gh %v: %w", args, err)
	}
	return out, nil
}

// ghSearchResult is the subset of `gh search prs|issues --json …` we map
// into an Item. The same shape covers PRs and issues; the kind is passed
// to item() so the same struct yields either type.
type ghSearchResult struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Body       string `json:"body"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// List surfaces the user's actionable GitHub work. When filter.Assigned
// (the UI's only mode), it unions FOUR `gh search` queries — open PRs and
// issues, authored by OR assigned to @me — deduped by (type,
// owner/repo#number); a PR both authored and assigned collapses to one.
// The number is unique only within a repo, so the external id is
// `owner/repo#number`. The unfiltered path lists open PRs alone.
func (g *GitHub) List(ctx context.Context, filter Filter) ([]Item, error) {
	if !filter.Assigned {
		return g.search(ctx, "prs", "pr")
	}
	queries := []struct {
		kind string // gh search subcommand: prs|issues
		typ  string // Item.Ref.Type: pr|issue
		role string // --author=@me | --assignee=@me
	}{
		{"prs", "pr", "--author=@me"},
		{"prs", "pr", "--assignee=@me"},
		{"issues", "issue", "--author=@me"},
		{"issues", "issue", "--assignee=@me"},
	}
	items := []Item{}
	seen := map[string]bool{}
	for _, q := range queries {
		batch, err := g.search(ctx, q.kind, q.typ, q.role)
		if err != nil {
			return nil, err
		}
		for _, it := range batch {
			if key := it.Ref.String(); !seen[key] {
				seen[key] = true
				items = append(items, it)
			}
		}
	}
	return items, nil
}

// search runs one `gh search <kind> --state=open …` query and maps each
// result to an Item of the given type. extra carries query flags such as
// --author=@me.
func (g *GitHub) search(ctx context.Context, kind, typ string, extra ...string) ([]Item, error) {
	args := append([]string{"search", kind, "--state=open", "--json", "number,title,url,body,repository"}, extra...)
	out, err := g.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var results []ghSearchResult
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	items := make([]Item, 0, len(results))
	for _, r := range results {
		items = append(items, r.item(typ))
	}
	return items, nil
}

// Get resolves a single item ref to an Item, dispatching on ref.Type:
// "pr" → `gh pr view`, "issue" → `gh issue view`. The external id is
// `owner/repo#number` (unique across repos); the view fetches the one
// item so a dispatch can read its vars.
func (g *GitHub) Get(ctx context.Context, ref items.ItemRef) (Item, error) {
	repo, num, ok := splitRef(ref.ExternalID)
	if !ok {
		return Item{}, fmt.Errorf("github: invalid ref %q: want owner/repo#number", ref.ExternalID)
	}
	var sub string
	switch ref.Type {
	case "pr":
		sub = "pr"
	case "issue":
		sub = "issue"
	default:
		return Item{}, fmt.Errorf("github: unknown ref type %q: want pr or issue", ref.Type)
	}
	// `gh pr|issue view` has no `repository` JSON field (unlike `gh
	// search`); the repo is already known from the ref, so we set it below.
	out, err := g.run(ctx, sub, "view", num, "--repo", repo, "--json", "number,title,url,body")
	if err != nil {
		return Item{}, err
	}
	var r ghSearchResult
	if err := json.Unmarshal(out, &r); err != nil {
		return Item{}, fmt.Errorf("parse gh output: %w", err)
	}
	r.Repository.NameWithOwner = repo
	return r.item(ref.Type), nil
}

// splitRef splits an `owner/repo#number` external id. Both halves must
// be non-empty.
func splitRef(externalID string) (repo, num string, ok bool) {
	repo, num, found := strings.Cut(externalID, "#")
	if !found || repo == "" || num == "" {
		return "", "", false
	}
	return repo, num, true
}

// item maps a fetched search result to the generic Item, the single
// source of the result→Item shape shared by List and Get. kind is "pr"
// or "issue"; it sets Ref.Type and the number var name (pr_number vs
// issue_number).
func (r ghSearchResult) item(kind string) Item {
	repo := r.Repository.NameWithOwner
	num := strconv.Itoa(r.Number)
	return Item{
		Ref:   items.ItemRef{Source: "github", Type: kind, ExternalID: repo + "#" + num},
		Title: r.Title,
		Body:  r.Body,
		URL:   r.URL,
		Vars: map[string]string{
			"repo":           repo,
			kind + "_number": num,
			"url":            r.URL,
			"title":          r.Title,
			// Always present (empty when there is no body) — see the Linear
			// source's item() for why.
			"body": r.Body,
		},
	}
}
