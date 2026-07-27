package source

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/engine"
)

// fakeRunner records the args it was called with and replays a canned
// result, standing in for the real `gh` invocation.
type fakeRunner struct {
	out  []byte
	err  error
	args []string
}

func (f *fakeRunner) run(_ context.Context, args ...string) ([]byte, error) {
	f.args = args
	return f.out, f.err
}

const ghPRsJSON = `[
  {"number": 42, "title": "Fix login", "url": "https://github.com/allouis/attractor/pull/42", "repository": {"nameWithOwner": "allouis/attractor"}},
  {"number": 7, "title": "Add items view", "url": "https://github.com/foo/bar/pull/7", "repository": {"nameWithOwner": "foo/bar"}}
]`

func TestGitHubListParsesPRs(t *testing.T) {
	fr := &fakeRunner{out: []byte(ghPRsJSON)}
	src := &GitHub{run: fr.run}

	items, err := src.List(context.Background(), Filter{Assigned: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	want := Item{
		Ref:   engine.ItemRef{Source: "github", Type: "pr", ExternalID: "allouis/attractor#42"},
		Title: "Fix login",
		URL:   "https://github.com/allouis/attractor/pull/42",
		Vars: map[string]string{
			"repo":      "allouis/attractor",
			"pr_number": "42",
			"url":       "https://github.com/allouis/attractor/pull/42",
			"title":     "Fix login",
		},
	}
	if !reflect.DeepEqual(items[0], want) {
		t.Errorf("item[0] =\n %+v\nwant\n %+v", items[0], want)
	}
}

func TestGitHubListFilterAssigned(t *testing.T) {
	fr := &fakeRunner{out: []byte("[]")}
	src := &GitHub{run: fr.run}
	if _, err := src.List(context.Background(), Filter{Assigned: true}); err != nil {
		t.Fatalf("List: %v", err)
	}
	joined := strings.Join(fr.args, " ")
	if !strings.Contains(joined, "--assignee=@me") {
		t.Errorf("gh args %q missing --assignee=@me", joined)
	}
	if !strings.Contains(joined, "search") || !strings.Contains(joined, "prs") {
		t.Errorf("gh args %q not a `search prs` call", joined)
	}
}

func TestGitHubListError(t *testing.T) {
	fr := &fakeRunner{err: errors.New("gh: not authenticated")}
	src := &GitHub{run: fr.run}
	if _, err := src.List(context.Background(), Filter{Assigned: true}); err == nil {
		t.Fatal("expected error when gh fails")
	}
}
