package attractor_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/handler"
	"github.com/allouis/attractor/internal/server"
)

// TestConfigAPI_Roundtrip exercises the C2 config surface over a real
// loopback socket: GET redacts the Linear key, PUT secret-merges (an
// empty key on re-save keeps the stored one) and persists, and GET /repos
// projects config.repos — the wire contract the Config view consumes.
func TestConfigAPI_Roundtrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	srv := newTestServer(t, server.DefaultHandlers(handler.Codergen{}))

	// PUT a document with a secret and a repo.
	doc := config.Document{
		DefaultProvider: "anthropic",
		Providers:       map[string]config.Provider{"anthropic": {Backend: "acp", Command: "claude-agent-acp"}},
		Linear:          config.LinearConfig{APIKey: "lin_secret"},
		Repos:           map[string]config.RepoConfig{"a/b": {Path: t.TempDir()}},
	}
	putConfig(t, srv.URL(), doc)

	// GET redacts the key but keeps everything else.
	body := getBody(t, srv.URL()+"/config")
	if strings.Contains(body, "lin_secret") {
		t.Errorf("GET /config leaked the Linear key: %s", body)
	}
	if !strings.Contains(body, `"api_key_set":true`) {
		t.Errorf("want api_key_set:true, got: %s", body)
	}

	// Re-PUT with an empty key (as the redacted UI form would): secret-merge
	// keeps the stored key.
	doc.Linear.APIKey = ""
	putConfig(t, srv.URL(), doc)
	got, err := config.LoadDocument(home)
	must(t, err)
	if got.Linear.APIKey != "lin_secret" {
		t.Errorf("secret-merge dropped the stored key, got %q", got.Linear.APIKey)
	}

	// GET /repos projects the registered repos.
	var repos []config.RepoRef
	must(t, json.Unmarshal([]byte(getBody(t, srv.URL()+"/repos")), &repos))
	if len(repos) != 1 || repos[0].Name != "a/b" {
		t.Errorf("GET /repos = %#v, want one a/b entry", repos)
	}
}

func putConfig(t *testing.T, base string, doc config.Document) {
	t.Helper()
	body, err := json.Marshal(doc)
	must(t, err)
	req, err := http.NewRequest(http.MethodPut, base+"/config", bytes.NewReader(body))
	must(t, err)
	resp, err := http.DefaultClient.Do(req)
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		rb, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT /config status = %d; body=%s", resp.StatusCode, rb)
	}
}

func getBody(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	must(t, err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s status = %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	must(t, err)
	return string(body)
}
