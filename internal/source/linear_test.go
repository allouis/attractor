package source

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/engine"
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
  {"id":"abc-1","identifier":"ENG-42","title":"Fix login","url":"https://linear.app/acme/issue/ENG-42"},
  {"id":"def-2","identifier":"ENG-7","title":"Items view","url":"https://linear.app/acme/issue/ENG-7"}
]}}}}`

func TestLinearListParsesIssues(t *testing.T) {
	fd := &fakeDoer{body: linearIssuesJSON}
	src := &Linear{apiKey: "lin_xyz", doer: fd}

	items, err := src.List(context.Background(), Filter{Assigned: true})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	want := Item{
		Ref:   engine.ItemRef{Source: "linear", Type: "issue", ExternalID: "abc-1"},
		Title: "Fix login",
		URL:   "https://linear.app/acme/issue/ENG-42",
		Vars: map[string]string{
			"url":        "https://linear.app/acme/issue/ENG-42",
			"title":      "Fix login",
			"identifier": "ENG-42",
		},
	}
	if !reflect.DeepEqual(items[0], want) {
		t.Errorf("item[0] =\n %+v\nwant\n %+v", items[0], want)
	}
}

const linearIssueJSON = `{"data":{"issue":{"id":"abc-1","identifier":"ENG-42","title":"Fix login","url":"https://linear.app/acme/issue/ENG-42"}}}`

func TestLinearGet(t *testing.T) {
	fd := &fakeDoer{body: linearIssueJSON}
	src := &Linear{apiKey: "lin_xyz", doer: fd}

	ref := engine.ItemRef{Source: "linear", Type: "issue", ExternalID: "abc-1"}
	item, err := src.Get(context.Background(), ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := Item{
		Ref:   ref,
		Title: "Fix login",
		URL:   "https://linear.app/acme/issue/ENG-42",
		Vars: map[string]string{
			"url":        "https://linear.app/acme/issue/ENG-42",
			"title":      "Fix login",
			"identifier": "ENG-42",
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
