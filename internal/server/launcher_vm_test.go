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

// The per-run jj workspace's `.jj/repo` is a RELATIVE pointer climbing out
// of the workspace dir to the host repo store — unusable in the guest, where
// only the mounts exist. W2 delivers the repo's `.jj` + colocated `.git`
// subtrees (NOT the repo root — the working tree stays off the guest) under
// guestRepoMount and repoints `.jj/repo` at the store inside, so in-guest jj
// (status/diff/commit) reaches the shared host store.
func TestWorkspaceStoreReachableInGuest(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeWorkspace(repo, work, "run-abc"); err != nil {
		t.Fatalf("materializeWorkspace: %v", err)
	}
	if err := pointGuestJJStore(work); err != nil {
		t.Fatalf("pointGuestJJStore: %v", err)
	}
	ptr, err := os.ReadFile(filepath.Join(work, ".jj", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if want := guestRepoMount + "/.jj/repo"; string(ptr) != want {
		t.Fatalf(".jj/repo = %q, want the store inside the guest repo mount %q", ptr, want)
	}

	l := vmLauncher{}
	byTag := map[string]virtiofsMount{}
	for _, m := range l.virtiofsMounts("/vm/abc", work, repo) {
		byTag[m.tag] = m
	}
	// The .jj store subtree and the colocated .git backend are delivered; the
	// repo ROOT is NOT, so the host working tree (secrets) never reaches the
	// guest.
	if got := byTag["repojj"].hostDir; got != filepath.Join(repo, ".jj") {
		t.Fatalf("repojj hostDir = %q, want %q (the store subtree, not the root)", got, filepath.Join(repo, ".jj"))
	}
	if got := byTag["repogit"].hostDir; got != filepath.Join(repo, ".git") {
		t.Fatalf("repogit hostDir = %q, want the colocated git backend %q", got, filepath.Join(repo, ".git"))
	}
	for _, m := range byTag {
		if m.hostDir == repo {
			t.Fatalf("a mount serves the repo ROOT %q — exposes the host working tree to the guest", repo)
		}
	}
}

// vmEnv points ATTRACTOR_WORKSPACE at the per-run jj workspace (NOT the
// host checkout — that was the old 9p rw mount) and carries the virtiofs
// QEMU_OPTS so run-nixos-vm boots the vhost-user-fs device. Guards against
// reintroducing a 9p rw workspace mount (spec W1 constraint).
func TestVMEnvUsesWorkspaceAndQemuOpts(t *testing.T) {
	l := vmLauncher{}
	env := l.vmEnv("/vm/abc", "/vm/abc/job", "/vm/abc/work", "-device vhost-user-fs-pci,tag=workspace")
	find := func(key string) string {
		for _, e := range env {
			if strings.HasPrefix(e, key+"=") {
				return strings.TrimPrefix(e, key+"=")
			}
		}
		return ""
	}
	if got := find("ATTRACTOR_WORKSPACE"); got != "/vm/abc/work" {
		t.Errorf("ATTRACTOR_WORKSPACE = %q, want the jj workspace path", got)
	}
	if got := find("QEMU_OPTS"); !strings.Contains(got, "vhost-user-fs-pci") {
		t.Errorf("QEMU_OPTS = %q, want the virtiofs device opts", got)
	}
	if got := find("NIX_DISK_IMAGE"); got != "/vm/abc/vm.qcow2" {
		t.Errorf("NIX_DISK_IMAGE = %q", got)
	}
}

// virtiofsdArgs pins cache=never (the Rust virtiofsd's name for the spec's
// "none": strongest coherence + locking — needed for the SQLite store and
// instant host visibility) and wires the per-run socket + shared dir.
func TestVirtiofsdArgs(t *testing.T) {
	args := strings.Join(virtiofsdArgs("/run/vfs.sock", "/vm/abc/work"), " ")
	for _, want := range []string{"--socket-path=/run/vfs.sock", "--shared-dir=/vm/abc/work", "--cache=never"} {
		if !strings.Contains(args, want) {
			t.Errorf("virtiofsdArgs missing %q in %q", want, args)
		}
	}
}

// virtiofsQemuOpts builds a vhost-user-fs device per mount + the single
// shared-memory backend they mandate, so the guest can mount each host dir
// rw over virtiofs (spec W1). Without memory-backend-memfd,share=on qemu
// refuses vhost-user-fs.
func TestVirtiofsQemuOpts(t *testing.T) {
	opts := virtiofsQemuOpts([]virtiofsMount{
		{tag: "workspace", hostDir: "/vm/abc/work", sock: "/run/vfs.sock"},
	}, 8192)
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

// Multiple mounts each get their own chardev+device pair but share the ONE
// memory-backend-memfd — qemu allows a single vhost-user shared-mem backend
// for all vhost-user-fs devices (spec W2: workspace + jj store mounts).
func TestVirtiofsQemuOptsMultipleMounts(t *testing.T) {
	opts := virtiofsQemuOpts([]virtiofsMount{
		{tag: "workspace", hostDir: "/vm/abc/work", sock: "/run/ws.sock"},
		{tag: "repojj", hostDir: "/repo/.jj", sock: "/run/jj.sock"},
		{tag: "repogit", hostDir: "/repo/.git", sock: "/run/git.sock"},
	}, 4096)
	for _, want := range []string{
		"-chardev socket,id=vfs-workspace,path=/run/ws.sock",
		"-device vhost-user-fs-pci,chardev=vfs-workspace,tag=workspace",
		"-chardev socket,id=vfs-repojj,path=/run/jj.sock",
		"-device vhost-user-fs-pci,chardev=vfs-repojj,tag=repojj",
		"-chardev socket,id=vfs-repogit,path=/run/git.sock",
		"-device vhost-user-fs-pci,chardev=vfs-repogit,tag=repogit",
		"-machine memory-backend=mem",
	} {
		if !strings.Contains(opts, want) {
			t.Errorf("virtiofsQemuOpts missing %q in %q", want, opts)
		}
	}
	if n := strings.Count(opts, "memory-backend-memfd"); n != 1 {
		t.Errorf("want exactly one shared memory backend, got %d in %q", n, opts)
	}
}

// jjInitRepo makes a colocated jj repo with one committed tracked file.
func jjInitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if out, err := exec.Command("jj", "git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("jj git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "foo.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("jj", "-R", repo, "describe", "-m", "init").CombinedOutput(); err != nil {
		t.Fatalf("jj describe: %v\n%s", err, out)
	}
	return repo
}

// A relaunch of the same run id must not be blocked by a leftover workspace
// of that name (e.g. the daemon crashed before cleanup ran): materialize
// forgets any stale run-<id> first, so `add` never dies with "already
// exists" (B3).
func TestMaterializeWorkspaceIdempotent(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	if err := materializeWorkspace(repo, filepath.Join(t.TempDir(), "w1"), "run-x"); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	if err := materializeWorkspace(repo, filepath.Join(t.TempDir(), "w2"), "run-x"); err != nil {
		t.Fatalf("relaunch same id must succeed (forget-before-add): %v", err)
	}
}

// A run that fails AFTER materializing (here: virtiofsd never serves) must
// still leave a vm.json so the reaper reclaims the work dir, qcow2, and jj
// workspace on retention — otherwise the reaper skips the dir and it leaks
// forever (B2).
func TestLaunchFailureRecordsForReaper(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	vmDir := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	run := srv.registry.NewRun("digraph{}", nil, nil, repo, nil, "", "", "", nil)
	run.cwd = repo
	l := vmLauncher{
		images: map[string]string{"default": "/nonexistent-runner"}, defaultImage: "default",
		vmDir: vmDir, guestHost: "10.0.2.2", pollInterval: time.Millisecond,
		virtiofsdBin: "false", memMiB: 4096, // `false` starts then exits → never serves the socket
	}
	if err := l.Launch(run, "http://127.0.0.1:0"); err == nil {
		t.Fatal("expected failure (virtiofsd never serves)")
	}
	if run.Status() != RunFailed {
		t.Fatalf("status = %v, want failed", run.Status())
	}
	data, err := os.ReadFile(filepath.Join(vmDir, run.ID, "vm.json"))
	if err != nil {
		t.Fatalf("failed run left no vm.json → reaper skips it → leak: %v", err)
	}
	var rec vmRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.RepoDir != repo || rec.Workspace != "run-"+run.ID {
		t.Fatalf("record missing repo/workspace for forget: %+v", rec)
	}
}

// A virtiofsd that dies AFTER boot leaves the guest mount hung: qemu keeps
// running, the run never phones home, and without a watch the launcher blocks
// forever. Launch must notice the daemon death, kill the VM, and fail the run.
func TestLaunchFailsWhenVirtiofsdDies(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	dir := t.TempDir()
	// Fake virtiofsd: create the requested socket (so startVirtiofsd proceeds
	// to boot) then die shortly after — a daemon that serves at boot but
	// crashes mid-run.
	vfsd := filepath.Join(dir, "fakevfsd.sh")
	writeExec(t, vfsd, "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in --socket-path=*) s=\"${a#--socket-path=}\";; esac; done\n: > \"$s\"\nsleep 0.3\n")
	// Fake VM: a long-running process that never phones home — a booted guest
	// whose mount just died.
	runner := filepath.Join(dir, "fakevm.sh")
	writeExec(t, runner, "#!/bin/sh\nsleep 30\n")

	repo := jjInitRepo(t)
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	run := srv.registry.NewRun("digraph{}", nil, nil, repo, nil, "", "", "", nil)
	run.cwd = repo
	l := vmLauncher{
		images: map[string]string{"default": runner}, defaultImage: "default",
		vmDir: t.TempDir(), guestHost: "10.0.2.2", pollInterval: 20 * time.Millisecond,
		virtiofsdBin: vfsd, memMiB: 1024,
	}

	done := make(chan error, 1)
	go func() { done <- l.Launch(run, "http://127.0.0.1:0") }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Launch returned nil; a mid-run virtiofsd death must fail the run")
		}
		if !strings.Contains(err.Error(), "virtiofsd") {
			t.Fatalf("error %q should name virtiofsd", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Launch hung after virtiofsd died — no daemon-death watch")
	}
	if run.Status() != RunFailed {
		t.Fatalf("status = %v, want failed", run.Status())
	}
}

// writeExec writes an executable script for the fake-daemon tests.
func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The vm launcher's per-run workspace needs a jj-colocated repo. A run
// whose cwd is a plain (non-jj) directory fails fast with a diagnosable
// error naming jj, rather than a cryptic `jj workspace add` stderr dump or
// a silently broken VM (spec W1: target repos are jj-colocated).
func TestVMLauncherRequiresJJRepo(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	plain := t.TempDir() // no `jj git init` — not a jj repo
	vmDir := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	run := srv.registry.NewRun("digraph{}", nil, nil, plain, nil, "", "", "", nil)
	run.cwd = plain
	l := vmLauncher{images: map[string]string{"default": "/nonexistent"}, defaultImage: "default", vmDir: vmDir, guestHost: "10.0.2.2", pollInterval: time.Millisecond}
	err := l.Launch(run, "http://127.0.0.1:0")
	if err == nil {
		t.Fatal("expected error for non-jj cwd")
	}
	if !strings.Contains(err.Error(), "jj") {
		t.Fatalf("error %q should name jj", err)
	}
	if run.Status() != RunFailed {
		t.Fatalf("status = %v, want failed", run.Status())
	}
	// Fail fast: the precheck fires before any run dir / job dir is created.
	if entries, _ := os.ReadDir(vmDir); len(entries) != 0 {
		t.Fatalf("precheck left side effects in vmDir: %v", entries)
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
