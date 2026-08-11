package source

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/items"
)

// fakeDoer replays a canned HTTP response and records the last request.
type fakeDoer struct {
	body string
	err  error
	req  *http.Request
	sent string
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.req = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		f.sent = string(b)
	}
	if f.err != nil {
		return nil, f.err
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(f.body)),
	}, nil
}

const linearIssuesJSON = `{"data":{"viewer":{"assignedIssues":{"nodes":[
  {"id":"abc-1","identifier":"ENG-42","title":"Fix login","url":"https://linear.app/acme/issue/ENG-42","description":"Login breaks on SSO."},
  {"id":"def-2","identifier":"ENG-7","title":"Items view","url":"https://linear.app/acme/issue/ENG-7"}
]}}}}`

func TestLinearListParsesIssues(t *testing.T) {
	fd := &fakeDoer{body: linearIssuesJSON}
	src := &Linear{apiKey: "lin_xyz", doer: fd}

	got, err := src.List(context.Background(), Filter{Assigned: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	want := Item{
		Ref:   items.ItemRef{Source: "linear", Type: "issue", ExternalID: "abc-1"},
		Title: "Fix login",
		Body:  "Login breaks on SSO.",
		URL:   "https://linear.app/acme/issue/ENG-42",
		Vars: map[string]string{
			"url":        "https://linear.app/acme/issue/ENG-42",
			"title":      "Fix login",
			"identifier": "ENG-42",
			"body":       "Login breaks on SSO.",
		},
	}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("item[0] =\n %+v\nwant\n %+v", got[0], want)
	}
	// An issue with no description still seeds body (empty) — `vars=` in
	// consuming pipelines treats it as a required input.
	if v, ok := got[1].Vars["body"]; !ok || v != "" {
		t.Errorf("descriptionless issue body var = %q, %v; want present and empty", v, ok)
	}
	if fd := src.doer.(*fakeDoer); !strings.Contains(fd.sent, "description") {
		t.Errorf("query should request description; sent: %s", fd.sent)
	}
}

const linearIssueJSON = `{"data":{"issue":{"id":"abc-1","identifier":"ENG-42","title":"Fix login","url":"https://linear.app/acme/issue/ENG-42","description":"Login breaks on SSO."}}}`

func TestLinearGet(t *testing.T) {
	fd := &fakeDoer{body: linearIssueJSON}
	src := &Linear{apiKey: "lin_xyz", doer: fd}

	ref := items.ItemRef{Source: "linear", Type: "issue", ExternalID: "abc-1"}
	item, err := src.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := Item{
		Ref:   ref,
		Title: "Fix login",
		Body:  "Login breaks on SSO.",
		URL:   "https://linear.app/acme/issue/ENG-42",
		Vars: map[string]string{
			"url":        "https://linear.app/acme/issue/ENG-42",
			"title":      "Fix login",
			"identifier": "ENG-42",
			"body":       "Login breaks on SSO.",
		},
	}
	if !reflect.DeepEqual(item, want) {
		t.Errorf("item =\n %+v\nwant\n %+v", item, want)
	}
	if !strings.Contains(fd.sent, "abc-1") {
		t.Errorf("request body missing issue id: %q", fd.sent)
	}
}

func TestLinearListAuthHeader(t *testing.T) {
	fd := &fakeDoer{body: `{"data":{"viewer":{"assignedIssues":{"nodes":[]}}}}`}
	src := &Linear{apiKey: "lin_xyz", doer: fd}
	if _, err := src.List(context.Background(), Filter{Assigned: true}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := fd.req.Header.Get("Authorization"); got != "lin_xyz" {
		t.Errorf("Authorization = %q, want %q", got, "lin_xyz")
	}
	if !strings.Contains(fd.sent, "assignedIssues") {
		t.Errorf("request body missing assignedIssues query: %q", fd.sent)
	}
}

func TestLinearListError(t *testing.T) {
	fd := &fakeDoer{err: errors.New("dial tcp: refused")}
	src := &Linear{apiKey: "lin_xyz", doer: fd}
	if _, err := src.List(context.Background(), Filter{Assigned: true}); err == nil {
		t.Fatal("expected error when the HTTP call fails")
	}
}

// TestLinearListFiltersResolvedIssues verifies Done (completed) and
// Canceled issues are dropped from the intake list.
func TestLinearListFiltersResolvedIssues(t *testing.T) {
	const body = `{"data":{"viewer":{"assignedIssues":{"nodes":[
	  {"id":"a","title":"Active","url":"u1","state":{"type":"started","name":"In Progress"}},
	  {"id":"b","title":"Done thing","url":"u2","state":{"type":"completed","name":"Done"}},
	  {"id":"c","title":"Backlog thing","url":"u3","state":{"type":"backlog","name":"Backlog"}},
	  {"id":"d","title":"Cancelled thing","url":"u4","state":{"type":"canceled","name":"Canceled"}}
	]}}}}`
	src := &Linear{apiKey: "k", doer: &fakeDoer{body: body}}
	got, err := src.List(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range got {
		titles = append(titles, it.Title)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 non-resolved issues, got %d: %v", len(got), titles)
	}
	for _, it := range got {
		if it.Title == "Done thing" || it.Title == "Cancelled thing" {
			t.Fatalf("resolved issue leaked into list: %q", it.Title)
		}
	}
	// The query must ask for state so the filter has data.
	if fd := src.doer.(*fakeDoer); !strings.Contains(fd.sent, "state") {
		t.Fatalf("query should request issue state; sent: %s", fd.sent)
	}
}
