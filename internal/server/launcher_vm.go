package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/allouis/attractor/internal/config"
)

// vmLauncher runs each pipeline inside its own NixOS VM (nix/vm-runner.nix).
// It writes a per-run job dir (job.json + source.dot) shared into the guest
// over 9p, boots the VM, and blocks until the run reports terminal via
// phone-home — then returns leaving the VM running so it persists for
// post-run inspection (a reaper GCs old ones). See docs/nix-vm-runner-spec.md.
type vmLauncher struct {
	images       map[string]string // image name -> run-nixos-vm boot script (.#vm-runner)
	defaultImage string            // image used when a run names none
	vmDir        string            // root for per-run qcow2 + job dirs
	guestHost    string            // host as seen from the guest (default 10.0.2.2)
	pollInterval time.Duration     // how often to check run terminal/cancel
}

// guestWorkDir is where the guest copies the ro-delivered workspace onto its
// OWN ext4 and runs the pipeline (cwd). A shared mount can't host the SQLite
// DBs real tools open (pnpm store index, Nx task DB) — they die with `disk I/O
// error` — so the mount is transport only and the work surface is guest-local
// (spec §Empirical pivot, G1). Coupled to the `cp` target in
// nix/vm-runner.nix; the two must agree, like /mnt/workspace did.
const guestWorkDir = "/work"

// NewVMLauncher returns a Launcher that boots nix/vm-runner.nix VMs from a
// single script, registered as the "default" image. runnerScript is the
// run-nixos-vm path (from `nix build .#vm-runner`); vmDir roots each run's
// qcow2 + job dir. For a multi-image registry use NewVMLauncherWithImages.
func NewVMLauncher(runnerScript, vmDir string) Launcher {
	return NewVMLauncherWithImages(map[string]string{"default": runnerScript}, "default", vmDir)
}

// NewVMLauncherWithImages returns a Launcher that resolves each run's boot
// script from a name -> script registry (per-repo VM config, VM1). A run's
// requested image name selects the script; an empty request uses
// defaultImage.
func NewVMLauncherWithImages(images map[string]string, defaultImage, vmDir string) Launcher {
	return vmLauncher{
		images:       images,
		defaultImage: defaultImage,
		vmDir:        vmDir,
		guestHost:    "10.0.2.2",
		pollInterval: 500 * time.Millisecond,
	}
}

// script resolves a run's requested image name to its boot script. An empty
// name falls back to the default image; an unknown name is an error listing
// the registered names so a misconfiguration is diagnosable.
func (l vmLauncher) script(imageName string) (string, error) {
	if imageName == "" {
		imageName = l.defaultImage
	}
	if s, ok := l.images[imageName]; ok {
		return s, nil
	}
	return "", fmt.Errorf("vm launcher: unknown image %q (registered: %s)", imageName, strings.Join(l.ImageNames(), ", "))
}

// HasImage reports whether name is a registered image (ImageValidator).
func (l vmLauncher) HasImage(name string) bool {
	_, ok := l.images[name]
	return ok
}

// ImageNames returns the registered image names, sorted, for diagnosable
// rejection messages (ImageValidator).
func (l vmLauncher) ImageNames() []string {
	names := make([]string, 0, len(l.images))
	for name := range l.images {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// vmJob is the per-run job the guest reads off the 9p job share.
type vmJob struct {
	RunID     string `json:"run_id"`
	Token     string `json:"token"`
	ReportURL string `json:"report_url"`
	Cwd       string `json:"cwd"`
	// BaseSubdir names this pipeline's dir within the catalog copied into
	// /mnt/job (empty for a raw-dot run). The guest sets `--base-dir
	// /mnt/job/<BaseSubdir>` so a manager_loop child like `../review-core`
	// resolves to a sibling that was delivered alongside it.
	BaseSubdir string            `json:"base_subdir,omitempty"`
	Vars       map[string]string `json:"vars,omitempty"`
}

// guestReportURL rewrites the daemon's reportURL host to the address the
// guest reaches the host on (10.0.2.2 over QEMU user-net, decision D7),
// preserving scheme, port, and path.
func guestReportURL(reportURL, guestHost string) string {
	u, err := url.Parse(reportURL)
	if err != nil || u.Host == "" {
		return reportURL
	}
	if p := u.Port(); p != "" {
		u.Host = guestHost + ":" + p
	} else {
		u.Host = guestHost
	}
	return u.String()
}

// writeJob materializes the per-run job dir (job.json + source.dot) that is
// 9p-shared into the guest at /mnt/job.
func (l vmLauncher) writeJob(run *Run, reportURL string) (jobDir string, err error) {
	runDir := filepath.Join(l.vmDir, run.ID)
	jobDir = filepath.Join(runDir, "job")
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return "", err
	}
	// Ship the whole pipeline CATALOG (this pipeline AND its siblings, with
	// their `prompts/` and @file deps) into the job share, so a
	// stack.manager_loop child like `../review-core` resolves in the guest —
	// the guest runs subgraphs IN-PROCESS and needs their files, not just the
	// parent's dir. The catalog entries are symlinks (into the repo / nix
	// store), so resolve to the real pipeline dir and copy its PARENT; the
	// guest points `--base-dir` at this pipeline's subdir within the copy
	// (BaseSubdir), from which `../sibling` resolves. Skipped for a raw-dot run
	// (base-dir is the working tree, already at /mnt/workspace).
	baseSubdir := ""
	if base := run.baseDir(); base != "" && base != run.cwd {
		real, err := filepath.EvalSymlinks(base)
		if err != nil {
			real = base
		}
		if err := copyTree(filepath.Dir(real), jobDir); err != nil {
			return "", fmt.Errorf("copy pipeline catalog: %w", err)
		}
		baseSubdir = filepath.Base(real)
	}
	job := vmJob{
		RunID:      run.ID,
		Token:      run.Token(),
		ReportURL:  guestReportURL(reportURL, l.guestHost),
		Cwd:        guestWorkDir,
		BaseSubdir: baseSubdir,
		Vars:       run.initialContext,
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(jobDir, "job.json"), data, 0o644); err != nil {
		return "", err
	}
	// Written last so our resolved source wins over any pipeline.dot copied
	// from the base-dir tree above.
	if err := os.WriteFile(filepath.Join(jobDir, "source.dot"), []byte(run.Source()), 0o644); err != nil {
		return "", err
	}
	return jobDir, nil
}

// copyTree copies the regular files and subdirectories of src into dst
// (which must already exist). Symlinks are skipped so a pipeline dir cannot
// smuggle a link that escapes the job share into the guest.
func copyTree(src, dst string) error {
	return copyTreeFiltered(src, dst, nil)
}

// copyTreeFiltered is copyTree with an optional per-entry skip predicate: when
// skip returns true for an entry's base name, that file or subtree is not
// copied. Symlinks are always skipped (as copyTree documents). Used to omit a
// submodule's own `.git` when copying its content into the workspace.
func copyTreeFiltered(src, dst string, skip func(name string) bool) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if skip != nil && skip(ent.Name()) {
			continue
		}
		sp := filepath.Join(src, ent.Name())
		dp := filepath.Join(dst, ent.Name())
		info, err := ent.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			continue
		case ent.IsDir():
			if err := os.MkdirAll(dp, 0o755); err != nil {
				return err
			}
			if err := copyTreeFiltered(sp, dp, skip); err != nil {
				return err
			}
		default:
			data, err := os.ReadFile(sp)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dp, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// submodulePaths parses a .gitmodules file for the `path = <p>` entries naming
// each submodule's location within the worktree, in file order. A missing file
// returns nil with no error — a repo with no submodules is the common case.
func submodulePaths(gitmodulesPath string) ([]string, error) {
	data, err := os.ReadFile(gitmodulesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "path" {
			continue
		}
		if p := strings.TrimSpace(val); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// copyHostSubmodules fills a materialized workspace's submodule dirs from the
// HOST checkout. jj tracks only a submodule's gitlink, so `jj workspace add`
// leaves its CONTENT absent — but the app needs it (Ghost's default themes live
// in submodules; it 500s without them). The host checkout (run.cwd) usually has
// the content already, so copy it in; the guest's network `submodule update`
// then only has to fetch the submodules the host lacked. A submodule whose host
// dir is missing or empty is skipped (left for the guest fetch); a repo with no
// .gitmodules is a no-op. The submodule's own `.git` (a gitlink file or nested
// repo) is not copied — the guest builds its own git metadata (vm-runner.nix).
func copyHostSubmodules(hostRepo, work string) error {
	paths, err := submodulePaths(filepath.Join(work, ".gitmodules"))
	if err != nil {
		return err
	}
	for _, p := range paths {
		src := filepath.Join(hostRepo, p)
		if !nonEmptyDir(src) {
			continue // host lacks it too — leave it for the guest's network fetch
		}
		dst := filepath.Join(work, p)
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if err := copyTreeFiltered(src, dst, func(name string) bool { return name == ".git" }); err != nil {
			return fmt.Errorf("copy submodule %s: %w", p, err)
		}
	}
	return nil
}

// nonEmptyDir reports whether path is a directory with at least one entry.
func nonEmptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	return err == nil && len(entries) > 0
}

// vmEnv builds the environment run-nixos-vm reads: the per-run qcow2 disk and
// the five 9p share sources the nix module's sharedDirectories expand —
// ATTRACTOR_JOB_DIR (job dir, ro), ATTRACTOR_WORKSPACE (per-run jj workspace,
// ro transport the guest copies to ext4), ATTRACTOR_REPO (the target repo
// whose `.jj`/`.git` subtrees are shared for in-guest jj), ATTRACTOR_CREDS_DIR
// (staged LLM oauth creds, ro, the guest copies into $HOME), and ATTRACTOR_LOGS
// (the daemon's run dir, shared rw at /mnt/runlogs so the guest writes the
// child's logs root — run.json, checkpoint.json, stage dirs — straight onto
// host disk for live tailing; single-writer-safe post-P5a, ui-run-view-v3 P5c).
// GS drops virtiofs, so there is no QEMU_OPTS: delivery rides the module's
// blessed 9p shares, not a hand-wired vhost-user-fs device.
func (l vmLauncher) vmEnv(runDir, jobDir, workspace, repoDir, credsDir, logsDir string) []string {
	return append(os.Environ(),
		"NIX_DISK_IMAGE="+filepath.Join(runDir, "vm.qcow2"),
		"ATTRACTOR_JOB_DIR="+jobDir,
		"ATTRACTOR_WORKSPACE="+workspace,
		"ATTRACTOR_REPO="+repoDir,
		"ATTRACTOR_CREDS_DIR="+credsDir,
		"ATTRACTOR_LOGS="+logsDir,
	)
}

// stageAgentCreds copies the daemon user's credential files — and nothing else
// — into dest, a per-run dir the guest gets over ro 9p, so in-guest tools can
// authenticate INSIDE the VM. Two classes: LLM oauth (`.claude`/`.codex`) so
// the bundled acp adapters reach session/new; and the `gh` token
// (`.config/gh/hosts.yml`) so an in-workflow `gh pr create`/push node can
// publish results. Only these files are staged — never the whole ~/.claude
// (history, memory, projects) or ~/.config/gh beyond hosts.yml — so untrusted
// in-guest code sees the tokens and nothing more. dest is always created —
// empty when the host isn't logged in — so the module's static /mnt/creds
// share always has a valid source. Paths are the same relative to the host ~
// and the guest $HOME, so the guest copies them straight in.
//
// NOTE (trust boundary): the gh token carries `repo` (read+WRITE) scope, so
// staging it lets untrusted in-guest code push to / open PRs in any repo the
// host user can write. That is the deliberate tradeoff for in-workflow PRs;
// per-node host placement (docs/vm-creds-spec.md) would scope it later.
func stageAgentCreds(dest string) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	_ = stageProviderConfig(dest)
	for _, rel := range []string{".claude/.credentials.json", ".codex/auth.json", ".config/gh/hosts.yml"} {
		data, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			continue // that provider isn't logged in on the host — skip it
		}
		out := filepath.Join(dest, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(out, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// stageProviderConfig delivers the daemon's provider routing — default_provider
// plus which acp command each llm_provider maps to — into dest at
// `.attractor/config.json`, so the guest's `attractor run` (which resolves a
// codergen node's llm_provider/llm_model to a command) finds it at
// $HOME/.attractor/config.json. Without it, provider routing in the guest falls
// back to the simulation backend (no-op agent nodes) or, under `--backend acp`
// with no --acp-cmd, fails "no agent command configured". ONLY the provider
// section crosses — never repos (host paths) or secrets like a Linear key. A
// missing host config is not fatal (the guest then simulates), so the caller
// ignores the error.
func stageProviderConfig(dest string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	doc, err := config.LoadDocument(home)
	if err != nil {
		return err
	}
	minimal := struct {
		DefaultProvider string                     `json:"default_provider"`
		Providers       map[string]config.Provider `json:"providers"`
	}{doc.DefaultProvider, doc.Providers}
	data, err := json.MarshalIndent(minimal, "", "  ")
	if err != nil {
		return err
	}
	out := filepath.Join(dest, ".attractor", "config.json")
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	return os.WriteFile(out, data, 0o600)
}

// ensureJJRepo verifies dir is a jj-COLOCATED checkout, the precondition for
// materializing a per-run jj workspace and for delivering the store to the
// guest. Two checks: `jj root` (it is a jj repo) AND a root `.git` (the store's
// colocated git backend). The nix module unconditionally 9p-shares
// $ATTRACTOR_REPO/.git; a jj repo with an external git backend has no root
// .git, so `jj root` alone would pass but that share's source is missing ->
// mnt-repo-.git.mount fails -> attractor-runner never starts -> no phone-home
// -> the launcher wait loop hangs forever. Reject it up front with a
// diagnosable error instead. Returns an error naming jj (or .git) when unmet.
func ensureJJRepo(dir string) error {
	cmd := exec.Command("jj", "-R", dir, "root")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vm launcher: %s is not a jj-colocated repo (the vm runner needs jj; see docs/vm-workspace-spec.md): %s", dir, strings.TrimSpace(string(out)))
	}
	if fi, err := os.Stat(filepath.Join(dir, ".git")); err != nil || !fi.IsDir() {
		return fmt.Errorf("vm launcher: %s has no colocated .git — the jj store's git backend must be colocated at the repo root (see docs/vm-workspace-spec.md); an external-backend repo can't deliver its store to the guest", dir)
	}
	return nil
}

// runWorkspaceName is the per-run jj workspace a launcher-backed run's commits
// land in within the shared host store (spec W1/W2). The vm launcher creates it
// (and the reaper forgets it); getRunDiff resolves the run's diff tip from it
// (`<name>@`). Shared so the two sides can never drift.
func runWorkspaceName(runID string) string { return "run-" + runID }

// materializeWorkspace adds an isolated jj workspace of repoDir at dest,
// named name, based on revision (spec W1). jj workspaces materialise only
// TRACKED files, so a gitignored node_modules never appears — the run
// installs its own, matching its own lockfile, and its mutations never touch
// the host checkout. The workspace lives on the host and shows in `jj log`,
// the property the 9p transport preserves.
//
// revision is the jj revset the workspace's @ is based on: "@" (the host
// repo's current working state) for a normal run, or a bookmark/rev like
// "hkg-1914" when a run targets an existing branch (revise-pr seeds
// workspace_revision; see workspaceRevision). The caller validates it
// resolves before we get here (ensureRevisionResolves).
func materializeWorkspace(repoDir, dest, name, revision string) error {
	// Idempotent: a leftover workspace of this name — a prior run whose
	// cleanup didn't complete (daemon crash) — would make `add` fail with
	// "already exists", permanently blocking a retry of the same run id
	// (B3). Forget any stale one first (ignoring "no such workspace").
	_ = forgetWorkspace(repoDir, name)
	// --revision bases the workspace on revision. "@" bases it on the host
	// repo's current working state (the default would base on @-, dropping
	// uncommitted work); a bookmark bases it on that branch's tip instead.
	cmd := exec.Command("jj", "-R", repoDir, "workspace", "add", "--name", name, "--revision", revision, dest)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("jj workspace add: %w\n%s", err, out)
	}
	return nil
}

// guestRepoMount is the guest dir under which the target repo's jj metadata
// is delivered: `.jj` (shared store + op-log) at guestRepoMount/.jj and the
// colocated `.git` (the jj store's object backend) at guestRepoMount/.git.
//
// We mount ONLY these two subtrees, never the repo root. The working tree —
// source, `.env` and other secrets, node_modules, uncommitted files — stays
// off the guest, so an in-guest agent or a pnpm postinstall script can
// neither read nor write it. Both subtrees are needed because the repos are
// jj-colocated: the store's Git backend points at the sibling `.git`
// (store/git_target = ../../../.git), so delivering `.jj` alone dangles that
// pointer ("not a git repository"). As siblings under guestRepoMount every
// internal relative path resolves and in-guest jj commits into the shared
// HOST store (spec W2 "In-guest VCS"). Must match nix/vm-runner.nix
// sharedDirectories "/mnt/repo/.jj" + "/mnt/repo/.git".
const guestRepoMount = "/mnt/repo"

// pointGuestJJStore rewrites a freshly-materialized workspace's `.jj/repo`
// pointer to the store inside the guest mount (guestRepoMount/.jj/repo). jj
// writes it as a RELATIVE path climbing out of the workspace dir to the host
// repo store — a path that does not exist in the guest, where only the mounts
// are visible.
//
// The rewritten value is guest-oriented, so it is NOT valid on the host: a
// host `jj` run FROM the workspace dir would fail. That is safe because
// nothing host-side does so — cleanup and result inspection use `jj -R
// <repo>` against the shared store (where the run's commits and bookmark
// live), and on any non-success exit Launch restores the original pointer so
// a kept-for-inspection workspace stays host-usable. Spec W2.
func pointGuestJJStore(work string) (prev []byte, err error) {
	return pointJJStore(work, guestRepoMount+"/.jj/repo")
}

// pointJJStore writes storePath into the workspace's `.jj/repo` pointer — the
// absolute path at which the store is reachable in the target environment
// (the guest mount in production; a real dir under test). It returns the
// pointer's previous contents so the caller can restore it.
func pointJJStore(work, storePath string) (prev []byte, err error) {
	ptr := filepath.Join(work, ".jj", "repo")
	prev, err = os.ReadFile(ptr)
	if err != nil {
		return nil, err
	}
	return prev, os.WriteFile(ptr, []byte(storePath), 0o644)
}

func (l vmLauncher) Launch(run *Run, reportURL string) error {
	if run.cwd == "" {
		run.failCrashed("vm launcher: run has no cwd (working tree) to share")
		return fmt.Errorf("vm launcher: empty cwd")
	}
	runnerScript, err := l.script(run.image)
	if err != nil {
		run.failCrashed(err.Error())
		return err
	}
	// The per-run workspace is a jj workspace, so the checkout must be
	// jj-colocated (spec W1). Precheck before creating any run/job dir so a
	// plain directory fails fast with a diagnosable error, not a cryptic
	// `jj workspace add` stderr dump halfway through setup. We do NOT fall
	// back to a 9p rw mount of the host checkout — the spec forbids it.
	if err := ensureJJRepo(run.cwd); err != nil {
		run.failCrashed(err.Error())
		return err
	}
	runDir := filepath.Join(l.vmDir, run.ID)
	jobDir, err := l.writeJob(run, reportURL)
	if err != nil {
		run.failCrashed("vm launcher: prepare job: " + err.Error())
		return err
	}

	// Materialise an isolated per-run jj workspace of the target repo and
	// deliver it over READ-ONLY 9p (not a 9p rw share of the host checkout): a
	// workspace holds only tracked files (no node_modules) and lives on the
	// host (visible in `jj log`). The guest copies it onto its own ext4 and
	// runs there — the shared mount is transport only (spec G1/GS).
	work := filepath.Join(runDir, "work")
	wsName := runWorkspaceName(run.ID)
	// A restart re-enters Launch with the previous attempt's workspace still on
	// disk and registered in the store (the reaper hasn't run). REUSE it: the
	// attempt committed its work into the shared store from that workspace, and
	// re-materializing would reset the workspace to run.cwd@ current, dropping
	// every commit the attempt made. The guest restores the working tree to the
	// workspace's @ (jj workspace update-stale, vm-runner.nix) so the resume sees
	// that committed work. A fresh run — or a restart after the reaper forgot the
	// workspace — materializes anew.
	if !(dirExists(work) && workspaceKnown(run.cwd, wsName)) {
		if err := materializeWorkspace(run.cwd, work, wsName, "@"); err != nil {
			run.failCrashed("vm launcher: materialize workspace: " + err.Error())
			return err
		}
	}
	// jj materialized only tracked files, so submodule CONTENT is absent (only
	// the gitlink is tracked). Copy it from the host checkout so the guest boots
	// with the themes/etc. present; the guest's network fetch then covers only
	// what the host lacked (vm-runner.nix).
	if err := copyHostSubmodules(run.cwd, work); err != nil {
		run.failCrashed("vm launcher: copy submodules: " + err.Error())
		return err
	}
	// Repoint the workspace's `.jj/repo` at the guest store mount so in-guest
	// jj reaches the host store through the /mnt/repo 9p share (spec W2). The
	// rewritten value is guest-only, so restore the original on any non-success
	// exit — a run kept for inspection (failure/cancel) then stays
	// host-jj-usable rather than carrying a dangling pointer through retention.
	origPtr, err := pointGuestJJStore(work)
	if err != nil {
		run.failCrashed("vm launcher: point guest jj store: " + err.Error())
		return err
	}
	restorePtr := func() { _ = os.WriteFile(filepath.Join(work, ".jj", "repo"), origPtr, 0o644) }

	// A workspace (and soon a qcow2) now exists on disk. Every NON-success exit
	// below — console/boot failure, crash-before-terminal, cancel — must leave
	// a vm.json so the reaper reclaims the work dir, qcow2, and jj workspace on
	// retention; without the record the reaper skips the dir and it leaks
	// forever (B2). Success flips `recorded` (writing the live qemu pid).
	recorded := false
	defer func() {
		if !recorded {
			// Non-success: the guest (if any) is gone, so restore the
			// host-valid pointer and record for the reaper.
			restorePtr()
			l.recordVM(vmRecord{RunID: run.ID, Dir: runDir, RepoDir: run.cwd, Workspace: wsName})
		}
	}()

	// Stage the LLM oauth creds so the image's bundled acp adapters can
	// authenticate inside the guest (docs/vm-creds-spec.md). Always produces a
	// dir (empty if the host has no creds) so the module's static creds share
	// has a valid source.
	credsDir := filepath.Join(runDir, "creds")
	if err := stageAgentCreds(credsDir); err != nil {
		run.failCrashed("vm launcher: stage agent creds: " + err.Error())
		return err
	}

	// Share the daemon's run dir into the guest rw as the child's logs root
	// (/mnt/runlogs), so the in-guest run writes run.json, checkpoint.json, and
	// stage stdout/stderr straight onto host disk — the daemon then tails them
	// live (P5d) instead of waiting for the per-stage phone-home upload. Safe
	// only because post-P5a every path here has one writer: the daemon owns
	// manifest.json / source.dot / events.jsonl (the child runs with
	// --no-event-log), the child owns everything else. The dir already exists
	// (the daemon stamped manifest.json/source.dot at run creation); create it
	// defensively so the module's rw share always has a valid source.
	logsDir := run.logsRoot
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		run.failCrashed("vm launcher: create shared logs dir: " + err.Error())
		return err
	}

	// Boot a FRESH disk: on a restart a crashed guest's qcow2 may hold a
	// half-written filesystem that would poison the resume, and run-nixos-vm
	// reuses NIX_DISK_IMAGE if it exists — so delete any stale one first. The
	// workspace (reused above) carries the run's committed work; the qcow2 is
	// throwaway per-boot scratch, safe to discard.
	_ = os.Remove(filepath.Join(runDir, "vm.qcow2"))

	cmd := exec.Command(runnerScript)
	cmd.Env = l.vmEnv(runDir, jobDir, work, run.cwd, credsDir, logsDir)
	console, err := os.Create(filepath.Join(runDir, "vm-console.log"))
	if err != nil {
		run.failCrashed("vm launcher: console: " + err.Error())
		return err
	}
	defer console.Close()
	cmd.Stdout = console
	cmd.Stderr = console
	if err := cmd.Start(); err != nil {
		run.failCrashed("vm launcher: boot: " + err.Error())
		return err
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	ticker := time.NewTicker(l.pollInterval)
	defer ticker.Stop()
	for {
		if run.isTerminal() {
			// The run finished (phoned home). Leave the VM running for
			// inspection; record its pid for the reaper.
			l.recordVM(vmRecord{RunID: run.ID, Pid: cmd.Process.Pid, Dir: runDir, RepoDir: run.cwd, Workspace: wsName})
			recorded = true // suppress the defer's failure record
			return nil
		}
		select {
		case werr := <-exited:
			if !run.isTerminal() {
				run.failCrashed(fmt.Sprintf("vm exited before completion: %v (see %s/vm-console.log)", werr, runDir))
			}
			return werr
		case <-ticker.C:
			if run.IsCancelled() {
				_ = cmd.Process.Kill()
				<-exited
				return nil
			}
		}
	}
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// workspaceKnown reports whether repoDir's jj store still registers a workspace
// named name. `jj workspace list` prints one "<name>: <commit>" line per known
// workspace; a restart reuses the workspace only while it is still registered
// (the reaper `jj workspace forget`s it on retention).
func workspaceKnown(repoDir, name string) bool {
	out, err := exec.Command("jj", "-R", repoDir, "workspace", "list").CombinedOutput()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), name+":") {
			return true
		}
	}
	return false
}

// forgetWorkspace deregisters a per-run jj workspace from repoDir's op-log
// (spec W1). Run before removing the workspace dir so the host repo does
// not accumulate stale workspaces pointing at deleted dirs. Idempotent: a
// name that is unknown (already forgotten) is not an error worth failing on.
func forgetWorkspace(repoDir, name string) error {
	cmd := exec.Command("jj", "-R", repoDir, "workspace", "forget", name)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("jj workspace forget %s: %w\n%s", name, err, out)
	}
	return nil
}

// vmRecord marks a persisted VM so the reaper can find and GC it: Pid is the
// qemu process it kills; RepoDir + Workspace let it `jj workspace forget` the
// per-run workspace before removing its dir (spec W1). GS dropped virtiofs, so
// the launcher no longer records any per-run daemons — delivery is plain 9p.
//
// VfsdPids/VfsdPid are READ-ONLY back-compat: a VM in flight across the
// GS upgrade left a vm.json naming its virtiofsd daemons; the reaper still
// kills them so they don't orphan. New records never set these; the fields can
// go once no pre-GS VM remains within the retention window.
type vmRecord struct {
	RunID     string    `json:"run_id"`
	Pid       int       `json:"pid"`
	VfsdPids  []int     `json:"vfsd_pids,omitempty"` // legacy (pre-GS) — reaped, never written
	VfsdPid   int       `json:"vfsd_pid,omitempty"`  // legacy single-daemon record
	Dir       string    `json:"dir"`
	RepoDir   string    `json:"repo_dir,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	StartedAt time.Time `json:"started_at"`
}

// recordVM writes the marker the reaper uses to GC persisted VMs. StartedAt
// is stamped here.
func (l vmLauncher) recordVM(rec vmRecord) {
	rec.StartedAt = time.Now()
	data, _ := json.MarshalIndent(rec, "", "  ")
	_ = os.WriteFile(filepath.Join(rec.Dir, "vm.json"), data, 0o644)
}
