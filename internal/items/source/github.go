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
		items = append(items, pr.item())
	}
	return items, nil
}

// Get resolves a single PR to an Item. The external id is
// `owner/repo#number` (unique across repos); `gh pr view` fetches the one
// PR so a dispatch can read its vars.
func (g *GitHub) Get(ctx context.Context, ref items.ItemRef) (Item, error) {
	repo, num, ok := splitPRRef(ref.ExternalID)
	if !ok {
		return Item{}, fmt.Errorf("github: invalid pr ref %q: want owner/repo#number", ref.ExternalID)
	}
	// `gh pr view` has no `repository` JSON field (unlike `gh search
	// prs`); the repo is already known from the ref, so we set it below.
	out, err := g.run(ctx, "pr", "view", num, "--repo", repo, "--json", "number,title,url")
	if err != nil {
		return Item{}, err
	}
	var pr ghPR
	if err := json.Unmarshal(out, &pr); err != nil {
		return Item{}, fmt.Errorf("parse gh output: %w", err)
	}
	pr.Repository.NameWithOwner = repo
	return pr.item(), nil
}

// splitPRRef splits an `owner/repo#number` external id. Both halves must
// be non-empty.
func splitPRRef(externalID string) (repo, num string, ok bool) {
	repo, num, found := strings.Cut(externalID, "#")
	if !found || repo == "" || num == "" {
		return "", "", false
	}
	return repo, num, true
}

// item maps a fetched PR to the generic Item, the single source of the
// PR→Item shape shared by List and Get.
func (pr ghPR) item() Item {
	repo := pr.Repository.NameWithOwner
	num := strconv.Itoa(pr.Number)
	return Item{
		Ref:   items.ItemRef{Source: "github", Type: "pr", ExternalID: repo + "#" + num},
		Title: pr.Title,
		URL:   pr.URL,
		Vars: map[string]string{
			"repo":      repo,
			"pr_number": num,
			"url":       pr.URL,
			"title":     pr.Title,
		},
	}
}
