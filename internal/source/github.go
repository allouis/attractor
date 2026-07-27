package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/fabro/attractor/internal/engine"
)

// GitHub lists pull requests via the machine-authed `gh` CLI
// (items-spec §8: the first source, PRs → review). The runner is
// injectable so tests feed canned JSON without touching the network.
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

// ghPR is the subset of `gh search prs --json …` we map into an Item.
type ghPR struct {
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// List fetches open PRs assigned to the authenticated user. The PR
// number is unique only within a repo, so the external id is
// `owner/repo#number`.
func (g *GitHub) List(ctx context.Context, filter Filter) ([]Item, error) {
	args := []string{"search", "prs", "--state=open", "--json", "number,title,url,repository"}
	if filter.Assigned {
		args = append(args, "--assignee=@me")
	}
	out, err := g.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var prs []ghPR
	if err := json.Unmarshal(out, &prs); err != nil {
		return nil, fmt.Errorf("parse gh output: %w", err)
	}
	items := make([]Item, 0, len(prs))
	for _, pr := range prs {
		repo := pr.Repository.NameWithOwner
		num := strconv.Itoa(pr.Number)
		items = append(items, Item{
			Ref:   engine.ItemRef{Source: "github", Type: "pr", ExternalID: repo + "#" + num},
			Title: pr.Title,
			URL:   pr.URL,
			Vars: map[string]string{
				"repo":      repo,
				"pr_number": num,
				"url":       pr.URL,
				"title":     pr.Title,
			},
		})
	}
	return items, nil
}
