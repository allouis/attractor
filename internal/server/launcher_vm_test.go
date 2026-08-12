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

	"github.com/allouis/attractor/internal/graph"
)

// writeJob must deliver the WHOLE pipeline catalog (the running pipeline AND
// its siblings) into the job share and name the running pipeline in
// base_subdir — so a stack.manager_loop child like `../review-core` resolves
// in the guest, which runs subgraphs in-process.
func TestWriteJobDeliversCatalogSiblings(t *testing.T) {
	catalog := t.TempDir()
	for _, name := range []string{"review-pr", "review-core"} {
		dir := filepath.Join(catalog, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "pipeline.dot"), []byte("digraph{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := vmLauncher{vmDir: t.TempDir(), guestHost: "10.0.2.2"}
	run := &Run{
		ID:     "abc",
		source: "digraph d {}",
		graph:  &graph.Graph{BaseDir: filepath.Join(catalog, "review-pr")},
		cwd:    t.TempDir(), // != baseDir so the catalog copy runs
	}
	jobDir, err := l.writeJob(run, "http://127.0.0.1:7681")
	if err != nil {
		t.Fatalf("writeJob: %v", err)
	}
	for _, rel := range []string{"review-pr/pipeline.dot", "review-core/pipeline.dot"} {
		if _, err := os.Stat(filepath.Join(jobDir, rel)); err != nil {
			t.Errorf("%s not delivered to the guest (manager_loop ../review-core would fail): %v", rel, err)
		}
	}
	var job vmJob
	data, err := os.ReadFile(filepath.Join(jobDir, "job.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	if job.BaseSubdir != "review-pr" {
		t.Errorf("base_subdir = %q, want review-pr (the running pipeline's subdir)", job.BaseSubdir)
	}
}

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
	if job.Cwd != guestWorkDir {
		t.Fatalf("cwd = %q, want the guest-local work dir %q", job.Cwd, guestWorkDir)
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
	if err := materializeWorkspace(repo, work, "run-abc", "@"); err != nil {
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
	if err := materializeWorkspace(repo, work, "run-abc", "@"); err != nil {
		t.Fatalf("materializeWorkspace: %v", err)
	}
	if _, err := pointGuestJJStore(work); err != nil {
		t.Fatalf("pointGuestJJStore: %v", err)
	}
	ptr, err := os.ReadFile(filepath.Join(work, ".jj", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if want := guestRepoMount + "/.jj/repo"; string(ptr) != want {
		t.Fatalf(".jj/repo = %q, want the store inside the guest repo mount %q", ptr, want)
	}
}

// vmEnv points ATTRACTOR_WORKSPACE at the per-run jj workspace (NOT the host
// checkout — that was the old 9p rw mount) and ATTRACTOR_REPO at the target
// repo whose `.jj`/`.git` the nix module 9p-shares for in-guest jj. GS drops
// virtiofs, so there is no QEMU_OPTS: the workspace + repo now ride the
// module's blessed 9p sharedDirectories, not a hand-wired vhost-user-fs
// device. Guards against reintroducing QEMU_OPTS/virtiofs delivery.
func TestVMEnvUsesWorkspaceAndRepo(t *testing.T) {
	l := vmLauncher{}
	env := l.vmEnv("/vm/abc", "/vm/abc/job", "/vm/abc/work", "/repo", "/vm/abc/creds", "/runs/abc")
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
	if got := find("ATTRACTOR_REPO"); got != "/repo" {
		t.Errorf("ATTRACTOR_REPO = %q, want the target repo path (for the 9p .jj/.git shares)", got)
	}
	for _, e := range env {
		if strings.HasPrefix(e, "QEMU_OPTS=") {
			t.Errorf("QEMU_OPTS set (%q); GS drops virtiofs — no vhost-user-fs device", e)
		}
	}
	if got := find("NIX_DISK_IMAGE"); got != "/vm/abc/vm.qcow2" {
		t.Errorf("NIX_DISK_IMAGE = %q", got)
	}
	if got := find("ATTRACTOR_CREDS_DIR"); got != "/vm/abc/creds" {
		t.Errorf("ATTRACTOR_CREDS_DIR = %q, want the staged-creds dir", got)
	}
	if got := find("ATTRACTOR_LOGS"); got != "/runs/abc" {
		t.Errorf("ATTRACTOR_LOGS = %q, want the daemon run dir shared rw as the child logs root", got)
	}
}

// stageAgentCreds copies ONLY the oauth credential files into the per-run creds
// dir, preserving their path relative to ~, and copies nothing else from
// ~/.claude (history/memory/projects stay on the host). The dir is always
// created even with no creds, so the module's static /mnt/creds share has a
// valid source.
func TestStageAgentCredsCopiesOnlyCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Host ~/.claude with a credential file AND sensitive non-cred files, plus
	// the gh token (in-workflow push/PR) and a non-cred gh config file.
	mustWrite(t, filepath.Join(home, ".claude", ".credentials.json"), `{"token":"x"}`)
	mustWrite(t, filepath.Join(home, ".claude", "history.jsonl"), "secret history")
	mustWrite(t, filepath.Join(home, ".claude", "projects", "p.json"), "secret project")
	mustWrite(t, filepath.Join(home, ".config", "gh", "hosts.yml"), "github.com:\n  oauth_token: gho_x")
	mustWrite(t, filepath.Join(home, ".config", "gh", "config.yml"), "editor: vim")

	dest := filepath.Join(t.TempDir(), "creds")
	if err := stageAgentCreds(dest); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, ".claude", ".credentials.json")); err != nil || string(got) != `{"token":"x"}` {
		t.Fatalf("credentials not staged: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, ".config", "gh", "hosts.yml")); err != nil || string(got) == "" {
		t.Fatalf("gh token not staged: %q err=%v", got, err)
	}
	// Only hosts.yml crosses from ~/.config/gh — not config.yml or anything else.
	if _, err := os.Stat(filepath.Join(dest, ".config", "gh", "config.yml")); !os.IsNotExist(err) {
		t.Errorf(".config/gh/config.yml leaked into the guest (err=%v); only hosts.yml may cross", err)
	}
	// Non-credential files must NOT be copied.
	for _, leaked := range []string{".claude/history.jsonl", ".claude/projects/p.json"} {
		if _, err := os.Stat(filepath.Join(dest, leaked)); !os.IsNotExist(err) {
			t.Errorf("%s leaked into the guest creds share (err=%v); only credential files may cross", leaked, err)
		}
	}
	// codex absent on this host → skipped, not an error; dir still exists.
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("creds dir must always exist as the share source: %v", err)
	}
}

// mustWrite writes data to path, creating parent dirs.
func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
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
	if err := materializeWorkspace(repo, filepath.Join(t.TempDir(), "w1"), "run-x", "@"); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	if err := materializeWorkspace(repo, filepath.Join(t.TempDir(), "w2"), "run-x", "@"); err != nil {
		t.Fatalf("relaunch same id must succeed (forget-before-add): %v", err)
	}
}

// A run that fails AFTER materializing (here: the boot script does not
// exist) must still leave a vm.json so the reaper reclaims the work dir,
// qcow2, and jj workspace on retention — otherwise the reaper skips the dir
// and it leaks forever (B2).
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
	}
	if err := l.Launch(run, "http://127.0.0.1:0"); err == nil {
		t.Fatal("expected failure (boot script missing)")
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

// TestGuestJJStoreMechanism proves the guest-jj-to-host-store path WITHOUT a
// VM, so the normal `go test` gate exercises it (TestVMWorkspaceAcceptance,
// which proves the SQLite-on-ext4 half, is gated behind ATTRACTOR_VM_E2E and
// never runs in CI). It reproduces exactly what in-guest jj does: rewrite
// the workspace pointer to where the store is mounted, then commit through it
// and assert the commit lands in the HOST repo. The two subtree mounts are
// stood in for by symlinks (.jj + .git as siblings under a mount root), the
// same layout /mnt/repo/.jj + /mnt/repo/.git give the guest.
func TestGuestJJStoreMechanism(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeWorkspace(repo, work, "run-xyz", "@"); err != nil {
		t.Fatalf("materializeWorkspace: %v", err)
	}
	// Stand in for the guest's two subtree mounts: .jj and .git reachable as
	// siblings under a mount root, exactly as /mnt/repo/.jj + /mnt/repo/.git.
	mnt := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".jj"), filepath.Join(mnt, ".jj")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".git"), filepath.Join(mnt, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := pointJJStore(work, filepath.Join(mnt, ".jj", "repo")); err != nil {
		t.Fatalf("pointJJStore: %v", err)
	}

	// A jj commit through the rewritten pointer, as the guest makes it.
	if out, err := exec.Command("jj", "-R", work, "describe", "-m", "guest-mechanism ran").CombinedOutput(); err != nil {
		t.Fatalf("jj describe through rewritten pointer failed — store unreachable (colocated .git not resolving?): %v\n%s", err, out)
	}
	// It must have landed in the HOST repo store.
	out, err := exec.Command("jj", "-R", repo, "log", "--no-graph", "--ignore-working-copy", "-r", "all()", "-T", `description ++ "\n"`).CombinedOutput()
	if err != nil {
		t.Fatalf("host jj log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "guest-mechanism ran") {
		t.Fatalf("commit did not land in the host store:\n%s", out)
	}
}

// G1: the guest copies the ro-delivered workspace onto its own ext4 (/work)
// and runs all tools there, because SQLite (pnpm store index, Nx task DB)
// can't live on a shared 9p mount (spec §Empirical pivot). This proves the
// COPY does not break in-guest jj: a workspace copied to a new path, with its
// `.jj/repo` pointed at the store mount, still commits into the shared HOST
// store. Reproduces the guest step (`cp -a /mnt/workspace/. /work/`) WITHOUT a
// VM, so the normal `go test` gate covers it (the SQLite-on-ext4 half needs a
// real VM — TestVMWorkspaceAcceptance, gated). Mirrors
// TestGuestJJStoreMechanism with the copy inserted.
func TestGuestLocalWorkspaceCopyJJ(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	work := filepath.Join(t.TempDir(), "work")
	if err := materializeWorkspace(repo, work, "run-copy", "@"); err != nil {
		t.Fatalf("materializeWorkspace: %v", err)
	}
	// The guest's copy onto ext4: `cp -a /mnt/workspace/. /work/`. A distinct
	// path from `work` — the whole point is that jj still works after the move.
	local := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-a", work+"/.", local).CombinedOutput(); err != nil {
		t.Fatalf("cp -a workspace: %v\n%s", err, out)
	}
	// Stand in for the guest's two subtree mounts: .jj + .git as siblings under
	// a mount root, exactly as /mnt/repo/.jj + /mnt/repo/.git.
	mnt := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".jj"), filepath.Join(mnt, ".jj")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, ".git"), filepath.Join(mnt, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := pointJJStore(local, filepath.Join(mnt, ".jj", "repo")); err != nil {
		t.Fatalf("pointJJStore: %v", err)
	}

	// A jj commit through the copied workspace, as the guest makes it in /work.
	if out, err := exec.Command("jj", "-R", local, "describe", "-m", "copied-workspace ran").CombinedOutput(); err != nil {
		t.Fatalf("jj describe in the copied workspace failed — copy broke jj (stale workspace? unresolved store?): %v\n%s", err, out)
	}
	// It must have landed in the HOST repo store.
	out, err := exec.Command("jj", "-R", repo, "log", "--no-graph", "--ignore-working-copy", "-r", "all()", "-T", `description ++ "\n"`).CombinedOutput()
	if err != nil {
		t.Fatalf("host jj log: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "copied-workspace ran") {
		t.Fatalf("commit from the copied workspace did not land in the host store:\n%s", out)
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

// The nix module unconditionally 9p-shares $ATTRACTOR_REPO/.git (the jj
// store's colocated git backend). A jj repo with an EXTERNAL git backend has
// no root .git, so `jj root` succeeds but that share's source is missing ->
// mnt-repo-.git.mount fails -> attractor-runner (which requires it) never
// starts -> no phone-home -> the launcher's wait loop hangs forever. Launch
// must reject a non-colocated repo up front, naming .git, with no side
// effects — not boot a VM that can only hang.
func TestVMLauncherRequiresColocatedGit(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	// A jj repo whose git backend lives elsewhere: `jj root` works, but there
	// is no colocated root .git. Built jj-only from a throwaway colocated repo.
	backing := t.TempDir()
	if out, err := exec.Command("jj", "git", "init", "--colocate", backing).CombinedOutput(); err != nil {
		t.Fatalf("jj git init --colocate: %v\n%s", err, out)
	}
	repo := filepath.Join(t.TempDir(), "external")
	if out, err := exec.Command("jj", "git", "init", "--git-repo="+filepath.Join(backing, ".git"), repo).CombinedOutput(); err != nil {
		t.Fatalf("jj git init --git-repo: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); !os.IsNotExist(err) {
		t.Fatalf("fixture has a root .git (err %v); test needs a non-colocated repo", err)
	}

	vmDir := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	run := srv.registry.NewRun("digraph{}", nil, nil, repo, nil, "", "", "", nil)
	run.cwd = repo
	l := vmLauncher{images: map[string]string{"default": "/nonexistent"}, defaultImage: "default", vmDir: vmDir, guestHost: "10.0.2.2", pollInterval: time.Millisecond}
	err := l.Launch(run, "http://127.0.0.1:0")
	if err == nil {
		t.Fatal("expected error for a non-colocated (no root .git) repo")
	}
	if !strings.Contains(err.Error(), ".git") {
		t.Fatalf("error %q should name the missing colocated .git", err)
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

// jjBranchedRepo makes a colocated jj repo with two sibling changes off a
// common base: a `feature` bookmark whose commit carries feature.txt, and a
// working copy (@) that carries at.txt instead — neither branch's distinctive
// file appears on the other. It lets a test prove a workspace is materialized
// at the requested revision rather than at @.
func jjBranchedRepo(t *testing.T) string {
	t.Helper()
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
	mustWrite(t, filepath.Join(repo, "base.txt"), "base")
	jj("describe", "-m", "base")
	jj("bookmark", "create", "base", "-r", "@")
	// feature branch: a child of base carrying feature.txt.
	jj("new", "-m", "feature")
	mustWrite(t, filepath.Join(repo, "feature.txt"), "on-feature")
	jj("bookmark", "create", "feature", "-r", "@")
	// working copy: a sibling child of base carrying at.txt (NOT feature.txt).
	jj("new", "base", "-m", "at-work")
	mustWrite(t, filepath.Join(repo, "at.txt"), "on-at")
	return repo
}

// materializeWorkspace bases the workspace on the revision it is given, so a
// run pointed at an existing branch (revise-pr seeds workspace_revision =
// the PR bookmark) sees that branch's file state — not the host repo's @.
func TestMaterializeWorkspaceAtRevision(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjBranchedRepo(t)

	feat := filepath.Join(t.TempDir(), "work")
	if err := materializeWorkspace(repo, feat, "run-feat", "feature"); err != nil {
		t.Fatalf("materializeWorkspace at feature: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(feat, "feature.txt")); err != nil || string(got) != "on-feature" {
		t.Fatalf("feature.txt = %q (err %v); workspace not at the feature bookmark", got, err)
	}
	if _, err := os.Stat(filepath.Join(feat, "at.txt")); !os.IsNotExist(err) {
		t.Fatalf("at.txt present in feature workspace (err %v); it materialized @'s state, not the bookmark's", err)
	}

	// @ (the default) still materializes the host working copy — at.txt, no
	// feature.txt — proving the param selects, not overrides unconditionally.
	atWork := filepath.Join(t.TempDir(), "work")
	if err := materializeWorkspace(repo, atWork, "run-at", "@"); err != nil {
		t.Fatalf("materializeWorkspace at @: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(atWork, "at.txt")); err != nil || string(got) != "on-at" {
		t.Fatalf("at.txt = %q (err %v); @ workspace missing the working-copy file", got, err)
	}
	if _, err := os.Stat(filepath.Join(atWork, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("feature.txt present in @ workspace (err %v)", err)
	}
}

// workspaceRevision reads the run's workspace_revision var (the bookmark/rev a
// revise-pr dispatch seeds); an empty or absent var keeps today's @.
func TestWorkspaceRevision(t *testing.T) {
	cases := []struct {
		name string
		ctx  map[string]string
		want string
	}{
		{"absent", nil, "@"},
		{"empty", map[string]string{"workspace_revision": ""}, "@"},
		{"bookmark", map[string]string{"workspace_revision": "hkg-1914"}, "hkg-1914"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			run := &Run{initialContext: c.ctx}
			if got := workspaceRevision(run); got != c.want {
				t.Fatalf("workspaceRevision = %q, want %q", got, c.want)
			}
		})
	}
}

// A run seeding a workspace_revision that does not resolve in the repo fails
// the launch fast with a diagnosable error naming the revision — before any
// run/job dir is created — rather than dying in a cryptic `jj workspace add`
// stderr dump or booting a VM that can only hang.
func TestVMLauncherWorkspaceRevisionUnknownFails(t *testing.T) {
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj not on PATH")
	}
	repo := jjInitRepo(t)
	vmDir := t.TempDir()
	srv := New(Config{Addr: "127.0.0.1:0", LogsRoot: t.TempDir()})
	run := srv.registry.NewRun("digraph{}", nil, nil, repo, nil, "", "", "",
		map[string]string{"workspace_revision": "no-such-bookmark-xyz"})
	run.cwd = repo
	l := vmLauncher{images: map[string]string{"default": "/nonexistent"}, defaultImage: "default", vmDir: vmDir, guestHost: "10.0.2.2", pollInterval: time.Millisecond}
	err := l.Launch(run, "http://127.0.0.1:0")
	if err == nil {
		t.Fatal("expected error for an unresolvable workspace_revision")
	}
	if !strings.Contains(err.Error(), "no-such-bookmark-xyz") {
		t.Fatalf("error %q should name the bad revision", err)
	}
	if run.Status() != RunFailed {
		t.Fatalf("status = %v, want failed", run.Status())
	}
	// Fail fast: the precheck fires before any run dir / job dir is created.
	if entries, _ := os.ReadDir(vmDir); len(entries) != 0 {
		t.Fatalf("precheck left side effects in vmDir: %v", entries)
	}
}
