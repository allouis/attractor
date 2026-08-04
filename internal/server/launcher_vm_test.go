package server

import (
	"encoding/json"
	"os"
	"os/exec"
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

// copyTree ships a pipeline's base-dir (its prompts/ and @file deps) into
// the job share, recursively, skipping symlinks. Without it a VM run of any
// pipeline that uses @prompts/… dies in the guest with "no such file".
func TestCopyTree(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "prompts", "plan.md"), []byte("plan!"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pipeline.dot"), []byte("digraph{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink must not be followed into the job share.
	_ = os.Symlink("/etc/passwd", filepath.Join(src, "sneaky"))

	dst := t.TempDir()
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "prompts", "plan.md"))
	if err != nil || string(got) != "plan!" {
		t.Fatalf("prompts/plan.md = %q (err %v)", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "sneaky")); !os.IsNotExist(err) {
		t.Fatalf("symlink was copied into the job share (err %v)", err)
	}
}

// materializeWorkspace adds an isolated jj workspace holding only the
// repo's TRACKED files — never gitignored node_modules — so a run mutates
// its own copy, not the host checkout (spec: per-run jj workspace, W1).
func TestMaterializeWorkspace(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := t.TempDir()
	jj := func(args ...string) {
		t.Helper()
		cmd := exec.Command("jj", append([]string{"-R", repo}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("jj %v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("jj", "git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("jj git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "foo.txt"), []byte("tracked"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "x.js"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	jj("describe", "-m", "init")

	work := filepath.Join(t.TempDir(), "work")
	if err := materializeWorkspace(repo, work, "run-abc"); err != nil {
		t.Fatalf("materializeWorkspace: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(work, "foo.txt")); err != nil || string(got) != "tracked" {
		t.Fatalf("foo.txt = %q (err %v); tracked file not materialized", got, err)
	}
	if _, err := os.Stat(filepath.Join(work, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("node_modules leaked into workspace (err %v); must install per run", err)
	}
	out, err := exec.Command("jj", "-R", repo, "workspace", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("workspace list: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "run-abc") {
		t.Fatalf("workspace list missing run-abc:\n%s", out)
	}
}

// virtiofsdArgs pins cache=none (strongest coherence + locking — needed
// for the SQLite store and instant host visibility, spec virtiofsd cache
// mode) and wires the per-run socket + shared dir.
func TestVirtiofsdArgs(t *testing.T) {
	args := strings.Join(virtiofsdArgs("/run/vfs.sock", "/vm/abc/work"), " ")
	for _, want := range []string{"--socket-path=/run/vfs.sock", "--shared-dir=/vm/abc/work", "--cache=none"} {
		if !strings.Contains(args, want) {
			t.Errorf("virtiofsdArgs missing %q in %q", want, args)
		}
	}
}

// virtiofsQemuOpts builds the vhost-user-fs device + the shared-memory
// backend it mandates, so the guest can mount the host workspace rw over
// virtiofs (spec W1). Without memory-backend-memfd,share=on qemu refuses
// vhost-user-fs.
func TestVirtiofsQemuOpts(t *testing.T) {
	opts := virtiofsQemuOpts("/run/vfs.sock", "workspace", 8192)
	for _, want := range []string{
		"-chardev socket,id=vfs-workspace,path=/run/vfs.sock",
		"-device vhost-user-fs-pci,chardev=vfs-workspace,tag=workspace",
		"-object memory-backend-memfd,id=mem,size=8192M,share=on",
		"-machine memory-backend=mem",
	} {
		if !strings.Contains(opts, want) {
			t.Errorf("virtiofsQemuOpts missing %q in %q", want, opts)
		}
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
