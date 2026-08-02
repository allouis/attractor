package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestVMLauncherResolvesImagePerRun(t *testing.T) {
	l := vmLauncher{
		images:       map[string]string{"default": "/a", "python": "/b"},
		defaultImage: "default",
	}
	cases := map[string]string{
		"python":  "/b",
		"default": "/a",
		"":        "/a", // empty request falls back to the default image
	}
	for name, want := range cases {
		got, err := l.script(name)
		if err != nil {
			t.Fatalf("script(%q): %v", name, err)
		}
		if got != want {
			t.Errorf("script(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestVMLauncherRejectsUnknownImage(t *testing.T) {
	l := vmLauncher{
		images:       map[string]string{"default": "/a", "python": "/b"},
		defaultImage: "default",
	}
	_, err := l.script("bogus")
	if err == nil {
		t.Fatal("expected error for unknown image")
	}
	for _, want := range []string{"bogus", "default", "python"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// The vm launcher reports its registered image names so dispatch can reject
// an unknown image at submit rather than at boot (VM3).
func TestVMLauncherImageValidator(t *testing.T) {
	var l ImageValidator = vmLauncher{
		images:       map[string]string{"default": "/a", "node-ts": "/b"},
		defaultImage: "default",
	}
	if !l.HasImage("node-ts") {
		t.Error("HasImage(node-ts) = false, want true")
	}
	if l.HasImage("bogus") {
		t.Error("HasImage(bogus) = true, want false")
	}
	if got := l.ImageNames(); !slices.Equal(got, []string{"default", "node-ts"}) {
		t.Errorf("ImageNames() = %v, want [default node-ts] (sorted)", got)
	}
}

func TestGuestReportURL(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:7681":   "http://10.0.2.2:7681",
		"http://127.0.0.1:7681/":  "http://10.0.2.2:7681/",
		"http://localhost:9000/x": "http://10.0.2.2:9000/x",
		"http://192.168.1.5":      "http://10.0.2.2",
		"https://host:443/api":    "https://10.0.2.2:443/api",
	}
	for in, want := range cases {
		if got := guestReportURL(in, "10.0.2.2"); got != want {
			t.Errorf("guestReportURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteJobMaterializesJobDir(t *testing.T) {
	vmDir := t.TempDir()
	l := vmLauncher{vmDir: vmDir, guestHost: "10.0.2.2"}
	run := &Run{
		ID:             "abc",
		token:          "tok",
		source:         "digraph d {}",
		initialContext: map[string]string{"k": "v"},
	}
	jobDir, err := l.writeJob(run, "http://127.0.0.1:7681")
	if err != nil {
		t.Fatalf("writeJob: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		t.Fatalf("read job.json: %v", err)
	}
	var job vmJob
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatalf("decode job.json: %v", err)
	}
	if job.RunID != "abc" || job.Token != "tok" {
		t.Fatalf("job = %+v", job)
	}
	if job.ReportURL != "http://10.0.2.2:7681" {
		t.Fatalf("report_url = %q, want guest-rewritten", job.ReportURL)
	}
	if job.Cwd != "/mnt/workspace" {
		t.Fatalf("cwd = %q", job.Cwd)
	}
	if job.Vars["k"] != "v" {
		t.Fatalf("vars = %v", job.Vars)
	}
	src, err := os.ReadFile(filepath.Join(jobDir, "source.dot"))
	if err != nil || string(src) != "digraph d {}" {
		t.Fatalf("source.dot = %q (err %v)", src, err)
	}
}

// A vm run with no working tree can't share a workspace; it fails fast
// rather than booting a useless VM.
func TestVMLauncherRequiresCwd(t *testing.T) {
	tmp := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: tmp})
	run := srv.registry.NewRun("digraph{}", nil, nil, tmp, nil, "", "", "", nil)
	l := vmLauncher{images: map[string]string{"default": "/nonexistent"}, defaultImage: "default", vmDir: t.TempDir(), guestHost: "10.0.2.2", pollInterval: time.Millisecond}
	if err := l.Launch(run, "http://127.0.0.1:0"); err == nil {
		t.Fatal("expected error for empty cwd")
	}
	if run.Status() != RunFailed {
		t.Fatalf("status = %v, want failed", run.Status())
	}
}
