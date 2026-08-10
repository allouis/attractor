package source

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/items"
)

// fakeRunner records the args it was called with and replays a canned
// result, standing in for the real `gh` invocation. Used where a single
// `gh` call is expected (Get, error path).
type fakeRunner struct {
	out  []byte
	err  error
	args []string
}

func (f *fakeRunner) run(_ context.Context, args ...string) ([]byte, error) {
	f.args = args
	return f.out, f.err
}

// keyedRunner replays canned JSON keyed by the (kind, role) of a
// `gh search` query, so List's four queries each get their own result.
// It records every call for query-set assertions.
type keyedRunner struct {
	responses map[string][]byte
	err       error
	calls     [][]string
}

// ghQueryKey reduces a `gh search prs|issues … --author|--assignee=@me`
// arg list to a "prs:author"-style key.
func ghQueryKey(args []string) string {
	var kind, role string
	for _, a := range args {
		switch a {
		case "prs":
			kind = "prs"
		case "issues":
			kind = "issues"
		case "--author=@me":
			role = "author"
		case "--assignee=@me":
			role = "assignee"
		}
	}
	return kind + ":" + role
}

func (r *keyedRunner) run(_ context.Context, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if r.err != nil {
		return nil, r.err
	}
	if out, ok := r.responses[ghQueryKey(args)]; ok {
		return out, nil
	}
	return []byte("[]"), nil
}

func (r *keyedRunner) keys() map[string]bool {
	seen := map[string]bool{}
	for _, c := range r.calls {
		seen[ghQueryKey(c)] = true
	}
	return seen
}

func prJSON(number int, title, url, repo string) string {
	return `[{"number": ` + strconv.Itoa(number) + `, "title": "` + title + `", "url": "` + url + `", "repository": {"nameWithOwner": "` + repo + `"}}]`
}

// TestGitHubListMineUnions checks that filter.Assigned issues the four
// `gh search` queries and unions PRs+issues with the right type and
// number var.
func TestGitHubListMineUnions(t *testing.T) {
	kr := &keyedRunner{responses: map[string][]byte{
		"prs:author":      []byte(prJSON(42, "Fix login", "https://github.com/allouis/attractor/pull/42", "allouis/attractor")),
		"prs:assignee":    []byte(prJSON(7, "Review me", "https://github.com/foo/bar/pull/7", "foo/bar")),
		"issues:author":   []byte(prJSON(100, "Doc bug", "https://github.com/allouis/attractor/issues/100", "allouis/attractor")),
		"issues:assignee": []byte(prJSON(200, "Assigned issue", "https://github.com/foo/bar/issues/200", "foo/bar")),
	}}
	src := &GitHub{run: kr.run}

	got, err := src.List(context.Background(), Filter{Assigned: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// All four queries issued.
	want := map[string]bool{"prs:author": true, "prs:assignee": true, "issues:author": true, "issues:assignee": true}
	if !reflect.DeepEqual(kr.keys(), want) {
		t.Errorf("queries = %v, want %v", kr.keys(), want)
	}

	byRef := map[string]Item{}
	for _, it := range got {
		byRef[it.Ref.String()] = it
	}
	if len(got) != 4 {
		t.Fatalf("got %d items, want 4: %+v", len(got), got)
	}

	pr := byRef["github:pr:allouis/attractor#42"]
	if pr.Ref.Type != "pr" || pr.Vars["pr_number"] != "42" || pr.Vars["repo"] != "allouis/attractor" {
		t.Errorf("pr item wrong: %+v", pr)
	}
	if _, ok := pr.Vars["issue_number"]; ok {
		t.Errorf("pr item carries issue_number: %+v", pr)
	}

	iss := byRef["github:issue:allouis/attractor#100"]
	if iss.Ref.Type != "issue" || iss.Vars["issue_number"] != "100" || iss.Vars["repo"] != "allouis/attractor" {
		t.Errorf("issue item wrong: %+v", iss)
	}
	if _, ok := iss.Vars["pr_number"]; ok {
		t.Errorf("issue item carries pr_number: %+v", iss)
	}
}

// TestGitHubListMineDedups checks an item returned by both the author and
// assignee query appears once.
func TestGitHubListMineDedups(t *testing.T) {
	same := prJSON(42, "Fix login", "https://github.com/allouis/attractor/pull/42", "allouis/attractor")
	kr := &keyedRunner{responses: map[string][]byte{
		"prs:author":   []byte(same),
		"prs:assignee": []byte(same),
	}}
	src := &GitHub{run: kr.run}

	got, err := src.List(context.Background(), Filter{Assigned: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1 (deduped): %+v", len(got), got)
	}
	if got[0].Ref.String() != "github:pr:allouis/attractor#42" {
		t.Errorf("item = %s, want github:pr:allouis/attractor#42", got[0].Ref.String())
	}
}

func TestGitHubListError(t *testing.T) {
	kr := &keyedRunner{err: errors.New("gh: not authenticated")}
	src := &GitHub{run: kr.run}
	if _, err := src.List(context.Background(), Filter{Assigned: true}); err == nil {
		t.Fatal("expected error when gh fails")
	}
}

const ghPRJSON = `{"number": 42, "title": "Fix login", "url": "https://github.com/allouis/attractor/pull/42", "repository": {"nameWithOwner": "allouis/attractor"}}`

func TestGitHubGet(t *testing.T) {
	fr := &fakeRunner{out: []byte(ghPRJSON)}
	src := &GitHub{run: fr.run}

	ref := items.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"}
	item, err := src.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := Item{
		Ref:   ref,
		Title: "Fix login",
		URL:   "https://github.com/allouis/attractor/pull/42",
		Vars: map[string]string{
			"repo":      "allouis/attractor",
			"pr_number": "42",
			"url":       "https://github.com/allouis/attractor/pull/42",
			"title":     "Fix login",
		},
	}
	if !reflect.DeepEqual(item, want) {
		t.Errorf("item =\n %+v\nwant\n %+v", item, want)
	}
	joined := strings.Join(fr.args, " ")
	if !strings.Contains(joined, "pr view") && !(strings.Contains(joined, "pr") && strings.Contains(joined, "view")) {
		t.Errorf("gh args %q not a `pr view` call", joined)
	}
	if !strings.Contains(joined, "--repo allouis/attractor") {
		t.Errorf("gh args %q missing --repo allouis/attractor", joined)
	}
	if !strings.Contains(joined, "42") {
		t.Errorf("gh args %q missing PR number", joined)
	}
}

const ghIssueJSON = `{"number": 100, "title": "Doc bug", "url": "https://github.com/allouis/attractor/issues/100"}`

func TestGitHubGetIssue(t *testing.T) {
	fr := &fakeRunner{out: []byte(ghIssueJSON)}
	src := &GitHub{run: fr.run}

	ref := items.ItemRef{Source: "github", Type: "issue", ExternalID: "allouis/attractor#100"}
	item, err := src.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := Item{
		Ref:   ref,
		Title: "Doc bug",
		URL:   "https://github.com/allouis/attractor/issues/100",
		Vars: map[string]string{
			"repo":         "allouis/attractor",
			"issue_number": "100",
			"url":          "https://github.com/allouis/attractor/issues/100",
			"title":        "Doc bug",
		},
	}
	if !reflect.DeepEqual(item, want) {
		t.Errorf("item =\n %+v\nwant\n %+v", item, want)
	}
	joined := strings.Join(fr.args, " ")
	if !(strings.Contains(joined, "issue") && strings.Contains(joined, "view")) {
		t.Errorf("gh args %q not an `issue view` call", joined)
	}
	if !strings.Contains(joined, "--repo allouis/attractor") {
		t.Errorf("gh args %q missing --repo allouis/attractor", joined)
	}
	if !strings.Contains(joined, "100") {
		t.Errorf("gh args %q missing issue number", joined)
	}
}

func TestGitHubGetBadRef(t *testing.T) {
	src := &GitHub{run: (&fakeRunner{out: []byte(ghPRJSON)}).run}
	if _, err := src.Get(context.Background(), items.ItemRef{Source: "github", Type: "pr", ExternalID: "no-hash"}); err == nil {
		t.Fatal("expected error for external id without owner/repo#number")
	}
}
