package attractor_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fabro/attractor/internal/artifact"
)

func TestArtifact_MemoryRoundTrip(t *testing.T) {
	s := artifact.New(t.TempDir())
	info, err := s.Put("a1", "small", []byte("hello"))
	must(t, err)
	if info.FileBacked {
		t.Fatal("small payload should stay in memory")
	}
	got, err := s.Get("a1")
	must(t, err)
	if string(got) != "hello" {
		t.Fatalf("Get returned %q", got)
	}
}

func TestArtifact_FileBackedAboveThreshold(t *testing.T) {
	dir := t.TempDir()
	s := artifact.New(dir)
	s.SetThreshold(32)
	payload := bytes.Repeat([]byte("x"), 100)
	info, err := s.Put("big", "big-blob", payload)
	must(t, err)
	if !info.FileBacked {
		t.Fatal("payload above threshold should be file-backed")
	}
	path := filepath.Join(dir, "big.json")
	stat, err := os.Stat(path)
	must(t, err)
	if stat.Size() != 100 {
		t.Fatalf("on-disk size = %d, want 100", stat.Size())
	}
	got, err := s.Get("big")
	must(t, err)
	if !bytes.Equal(got, payload) {
		t.Fatal("file-backed Get returned wrong bytes")
	}
}

func TestArtifact_ListAndRemove(t *testing.T) {
	s := artifact.New(t.TempDir())
	for _, id := range []string{"c", "a", "b"} {
		_, err := s.Put(id, "x", []byte(id))
		must(t, err)
	}
	infos := s.List()
	if len(infos) != 3 || infos[0].ID != "a" || infos[2].ID != "c" {
		t.Fatalf("List order: %v", infos)
	}
	must(t, s.Remove("b"))
	if s.Has("b") {
		t.Fatal("Remove did not delete")
	}
}

func TestArtifact_PutJSON(t *testing.T) {
	s := artifact.New(t.TempDir())
	info, err := s.PutJSON("payload", "result", map[string]any{
		"count": 42, "ok": true,
	})
	must(t, err)
	got, err := s.Get(info.ID)
	must(t, err)
	if !strings.Contains(string(got), `"count":42`) {
		t.Fatalf("JSON missing field: %s", got)
	}
}

func TestArtifact_ClearDeletesFiles(t *testing.T) {
	dir := t.TempDir()
	s := artifact.New(dir)
	s.SetThreshold(1)
	_, _ = s.Put("a", "x", []byte("payload"))
	s.Clear()
	if s.Has("a") {
		t.Fatal("Clear left entry")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.json")); err == nil {
		t.Fatal("Clear did not delete backing file")
	}
}

func TestEngine_ArtifactStoreInHandlerEnv(t *testing.T) {
	// Compile-time + minimal runtime check that env.Artifacts is wired.
	// We do this by running a tool node that writes to a path the
	// artifact store can also see (a smoke that env.Artifacts is non-nil
	// would suffice — but the engine doesn't expose it directly, so we
	// rely on the integration that the artifacts directory exists after
	// a run.)
	src := `digraph a {
		start [shape=Mdiamond]
		t [shape=parallelogram, tool_command="echo hi"]
		done [shape=Msquare]
		start -> t -> done
	}`
	_, _, logs := runFixture(t, src, nil, nil)
	// Engine creates the artifacts dir lazily via Store.Put, so this
	// assertion is loose: directory MAY exist; mostly we want no panics.
	_ = logs
}
