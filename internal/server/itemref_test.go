package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/fabro/attractor/internal/engine"
)

// TestNewRunStampsItemRef checks the item_ref supplied at creation is
// exposed in the run summary, and omitted when absent.
func TestNewRunStampsItemRef(t *testing.T) {
	reg := newRunRegistry(t.TempDir())
	ref := &engine.ItemRef{Source: "github", Type: "pr", ExternalID: "42"}

	run := reg.NewRun("", nil, nil, reg.baseDir, nil, ref)
	got, ok := run.Summary()["item_ref"]
	if !ok {
		t.Fatal("summary missing item_ref")
	}
	if got != *ref {
		t.Errorf("summary item_ref = %+v, want %+v", got, *ref)
	}

	bare := reg.NewRun("", nil, nil, reg.baseDir, nil, nil)
	if _, ok := bare.Summary()["item_ref"]; ok {
		t.Error("summary should omit item_ref when unset")
	}
}

// TestItemRefSurvivesReload checks the item_ref is durable: written to
// the manifest and restored when the registry reloads from disk.
func TestItemRefSurvivesReload(t *testing.T) {
	base := t.TempDir()
	reg := newRunRegistry(base)
	ref := &engine.ItemRef{Source: "github", Type: "pr", ExternalID: "7"}
	run := reg.NewRun("", nil, nil, base, nil, ref)
	id := run.ID

	reloaded := newRunRegistry(base)
	got, ok := reloaded.Get(id)
	if !ok {
		t.Fatalf("run %s missing after reload", id)
	}
	if v := got.Summary()["item_ref"]; v != *ref {
		t.Errorf("reloaded item_ref = %+v, want %+v", v, *ref)
	}
}

// TestSubmitPipelineAcceptsItemRef checks the API path: POST /pipelines
// with an item_ref stamps it on the run, readable via GET /pipelines/{id}.
func TestSubmitPipelineAcceptsItemRef(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	if err := srv.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// A minimal start->done graph completes instantly with no codergen
	// backend, so the run terminates deterministically before cleanup.
	dot := "digraph { start [shape=Mdiamond]; done [shape=Msquare]; start -> done }"
	body, _ := json.Marshal(map[string]any{
		"dot":      dot,
		"item_ref": engine.ItemRef{Source: "github", Type: "pr", ExternalID: "99"},
	})
	resp, err := http.Post(srv.URL()+"/pipelines", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("submit status = %d", resp.StatusCode)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	want := engine.ItemRef{Source: "github", Type: "pr", ExternalID: "99"}
	// Poll until terminal so the run's writer goroutine is done before
	// TempDir cleanup, and assert item_ref is exposed throughout.
	deadline := time.Now().Add(5 * time.Second)
	for {
		get, err := http.Get(srv.URL() + "/pipelines/" + created.ID)
		if err != nil {
			t.Fatal(err)
		}
		var summary struct {
			Status  string          `json:"status"`
			ItemRef *engine.ItemRef `json:"item_ref"`
		}
		if err := json.NewDecoder(get.Body).Decode(&summary); err != nil {
			get.Body.Close()
			t.Fatal(err)
		}
		get.Body.Close()
		if summary.ItemRef == nil || *summary.ItemRef != want {
			t.Fatalf("summary item_ref = %+v, want %+v", summary.ItemRef, want)
		}
		if summary.Status == string(RunCompleted) || summary.Status == string(RunFailed) || summary.Status == string(RunCancelled) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not terminate; last status = %q", summary.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
