package attractor_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

const automationDOT = `digraph triage {
	start [shape=Mdiamond]
	work [prompt="triage $label"]
	done [shape=Msquare]
	start -> work -> done
}`

// newAutomationServer stands up a server whose automations directory
// holds one automation ("triage") pointing at a pipeline .dot in the
// same temp dir.
func newAutomationServer(t *testing.T) (*server.Server, string) {
	t.Helper()
	dir := t.TempDir()
	autoDir := filepath.Join(dir, "automations")
	dotPath := filepath.Join(dir, "triage.dot")
	writeFile(t, dotPath, automationDOT)
	writeFile(t, filepath.Join(autoDir, "triage.toml"),
		"pipeline = \""+dotPath+"\"\ncwd = \""+dir+"\"\n[vars]\nlabel = \"bug\"\n[trigger]\ncron = \"0 3 * * *\"\n")

	srv := server.New(server.Config{
		Addr:           "127.0.0.1:0",
		LogsRoot:       t.TempDir(),
		MakeHandlers:   server.DefaultHandlers(handler.Codergen{}),
		AutomationsDir: autoDir,
	})
	must(t, srv.Start())
	t.Cleanup(func() { _ = srv.Close() })
	return srv, dir
}

func TestServer_RunAutomationManually(t *testing.T) {
	srv, _ := newAutomationServer(t)
	resp, err := http.Post(srv.URL()+"/automations/triage/run", "application/json", nil)
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("run status = %d", resp.StatusCode)
	}
	var out struct{ ID string }
	must(t, json.NewDecoder(resp.Body).Decode(&out))
	if out.ID == "" {
		t.Fatal("expected run id")
	}
	waitForRunStatus(t, srv, out.ID, "completed")
}

func TestServer_RunUnknownAutomation(t *testing.T) {
	srv, _ := newAutomationServer(t)
	resp, err := http.Post(srv.URL()+"/automations/nope/run", "application/json", nil)
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown automation status = %d, want 404", resp.StatusCode)
	}
}

func TestServer_ListAutomations(t *testing.T) {
	srv, _ := newAutomationServer(t)
	resp, err := http.Get(srv.URL() + "/automations")
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", resp.StatusCode)
	}
	var payload struct {
		Automations []struct {
			Name string `json:"name"`
			Cron string `json:"cron"`
		} `json:"automations"`
	}
	must(t, json.NewDecoder(resp.Body).Decode(&payload))
	if len(payload.Automations) != 1 || payload.Automations[0].Name != "triage" {
		t.Fatalf("automations = %+v", payload.Automations)
	}
	if payload.Automations[0].Cron != "0 3 * * *" {
		t.Fatalf("cron = %q", payload.Automations[0].Cron)
	}
}
