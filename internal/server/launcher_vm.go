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
	virtiofsdBin string            // virtiofsd executable (default "virtiofsd", found on PATH)
	memMiB       int               // guest RAM; sizes the shared-memory backend (must match nix memorySize)
}

// defaultVMMemoryMiB mirrors nix/vm-runner.nix virtualisation.memorySize;
// the vhost-user-fs shared-memory backend must be sized to the guest RAM.
const defaultVMMemoryMiB = 8192

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
		virtiofsdBin: "virtiofsd",
		memMiB:       defaultVMMemoryMiB,
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
	RunID     string            `json:"run_id"`
	Token     string            `json:"token"`
	ReportURL string            `json:"report_url"`
	Cwd       string            `json:"cwd"`
	Vars      map[string]string `json:"vars,omitempty"`
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
	// Ship the pipeline's base-dir (its `prompts/` and any other @file
	// dependencies) into the job share so `--base-dir /mnt/job` resolves them
	// in the guest. Without this a pipeline with `@prompts/…` dies in the VM
	// with "no such file". Skip when the base-dir is the working tree itself
	// (a raw-dot run): that whole tree is already mounted at /mnt/workspace,
	// and copying it into the job share would duplicate the entire repo.
	if base := run.baseDir(); base != "" && base != run.cwd {
		if err := copyTree(base, jobDir); err != nil {
			return "", fmt.Errorf("copy pipeline base-dir: %w", err)
		}
	}
	job := vmJob{
		RunID:     run.ID,
		Token:     run.Token(),
		ReportURL: guestReportURL(reportURL, l.guestHost),
		Cwd:       "/mnt/workspace",
		Vars:      run.initialContext,
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
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, ent := range entries {
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
			if err := copyTree(sp, dp); err != nil {
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

// vmEnv builds the environment run-nixos-vm reads: the per-run qcow2 disk,
// the 9p job-share source (ATTRACTOR_JOB_DIR, ro), the mutable working copy
// (ATTRACTOR_WORKSPACE — the per-run jj workspace, delivered over virtiofs),
// and QEMU_OPTS carrying the vhost-user-fs device the boot script appends.
func (l vmLauncher) vmEnv(runDir, jobDir, workspace, qemuOpts string) []string {
	env := append(os.Environ(),
		"NIX_DISK_IMAGE="+filepath.Join(runDir, "vm.qcow2"),
		"ATTRACTOR_JOB_DIR="+jobDir,
		"ATTRACTOR_WORKSPACE="+workspace,
	)
	if qemuOpts != "" {
		env = append(env, "QEMU_OPTS="+qemuOpts)
	}
	return env
}

// ensureJJRepo verifies dir is a jj repo (colocated checkout), the
// precondition for materializing a per-run jj workspace. Returns a
// diagnosable error naming jj when it is not.
func ensureJJRepo(dir string) error {
	cmd := exec.Command("jj", "-R", dir, "root")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vm launcher: %s is not a jj-colocated repo (the vm runner needs jj; see docs/vm-workspace-spec.md): %s", dir, strings.TrimSpace(string(out)))
	}
	return nil
}

// materializeWorkspace adds an isolated jj workspace of repoDir at dest,
// named name (spec W1). jj workspaces materialise only TRACKED files, so a
// gitignored node_modules never appears — the run installs its own,
// matching its own lockfile, and its mutations never touch the host
// checkout. The workspace lives on the host and shows in `jj log`, the
// property virtiofs delivery preserves.
func materializeWorkspace(repoDir, dest, name string) error {
	// Idempotent: a leftover workspace of this name — a prior run whose
	// cleanup didn't complete (daemon crash) — would make `add` fail with
	// "already exists", permanently blocking a retry of the same run id
	// (B3). Forget any stale one first (ignoring "no such workspace").
	_ = forgetWorkspace(repoDir, name)
	// --revision @ bases the workspace on the host repo's current working
	// state (default would base on @-, dropping uncommitted work).
	cmd := exec.Command("jj", "-R", repoDir, "workspace", "add", "--name", name, "--revision", "@", dest)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("jj workspace add: %w\n%s", err, out)
	}
	return nil
}

// virtiofsMount is one host dir delivered into the guest over virtiofs: a
// per-run virtiofsd serves hostDir on sock, exported to qemu under tag; the
// guest mounts tag rw (nix/vm-runner.nix fileSystems). A run has one mount
// per shared dir (workspace, jj store, …), each its own daemon + socket.
type virtiofsMount struct {
	tag     string // vhost-user-fs tag the guest mounts by
	hostDir string // dir this mount's virtiofsd serves
	sock    string // per-mount unix socket virtiofsd listens on
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
// virtualisation.fileSystems."/mnt/repo/.jj" + ".git".
const guestRepoMount = "/mnt/repo"

// virtiofsMounts is the set of host dirs delivered into the guest over
// virtiofs for a run: the per-run workspace working copy (rw at
// /mnt/workspace), and the target repo's `.jj` + colocated `.git` (rw under
// guestRepoMount) — the shared jj store the workspace commits into. Only
// those two repo subtrees are shared, never the repo root, so the host
// working tree (secrets, node_modules) is never exposed to the guest.
//
// Concurrency: multiple same-repo runs share this one store over virtiofs;
// safe concurrent multi-writer access is proven in W4. Until then the safe
// operating assumption is one live run per repo (spec Milestones: W4).
func (l vmLauncher) virtiofsMounts(runDir, work, repoDir string) []virtiofsMount {
	return []virtiofsMount{
		{tag: "workspace", hostDir: work, sock: filepath.Join(runDir, "vfs-workspace.sock")},
		{tag: "repojj", hostDir: filepath.Join(repoDir, ".jj"), sock: filepath.Join(runDir, "vfs-repojj.sock")},
		{tag: "repogit", hostDir: filepath.Join(repoDir, ".git"), sock: filepath.Join(runDir, "vfs-repogit.sock")},
	}
}

// pointGuestJJStore rewrites a freshly-materialized workspace's `.jj/repo`
// pointer to the store inside the guest repo-root mount. jj writes it as a
// RELATIVE path climbing out of the workspace dir to the host repo store — a
// path that does not exist in the guest, where only the mounts are visible.
// The host never runs jj INSIDE the workspace dir (cleanup and result
// inspection use `jj -R <repo>`), so overwriting the pointer for the guest's
// view is safe. Spec W2.
func pointGuestJJStore(work string) error {
	return os.WriteFile(filepath.Join(work, ".jj", "repo"), []byte(guestRepoMount+"/.jj/repo"), 0o644)
}

// virtiofsdArgs builds the per-run virtiofsd invocation: serve sharedDir
// over a unix socket with cache=never — the strongest coherence + locking
// mode, required for the SQLite store index and instant host visibility.
// The spec calls this "none" (the C virtiofsd's name); the Rust virtiofsd
// we ship (pkgs.virtiofsd) spells the same policy "never" — "none" is
// rejected as an invalid cache policy. W2 may relax to "auto" if the lock
// test holds (spec: virtiofsd cache mode).
func virtiofsdArgs(sock, sharedDir string) []string {
	return []string{
		"--socket-path=" + sock,
		"--shared-dir=" + sharedDir,
		"--cache=never",
	}
}

// virtiofsQemuOpts builds the QEMU_OPTS run-nixos-vm appends to boot with a
// vhost-user-fs device per mount, each backed by its virtiofsd socket and
// exported under its tag. vhost-user-fs requires a shared-memory backend, so
// we add one memory-backend-memfd (share=on) sized to the guest RAM and
// select it as the machine's memory — all devices share the single backend.
// The guest mounts each tag rw (nix/vm-runner.nix). Spec W1/W2.
func virtiofsQemuOpts(mounts []virtiofsMount, memMiB int) string {
	opts := make([]string, 0, len(mounts)*2+2)
	for _, m := range mounts {
		id := "vfs-" + m.tag
		opts = append(opts,
			fmt.Sprintf("-chardev socket,id=%s,path=%s", id, m.sock),
			fmt.Sprintf("-device vhost-user-fs-pci,chardev=%s,tag=%s", id, m.tag),
		)
	}
	opts = append(opts,
		fmt.Sprintf("-object memory-backend-memfd,id=mem,size=%dM,share=on", memMiB),
		"-machine memory-backend=mem",
	)
	return strings.Join(opts, " ")
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
	// back to a 9p rw mount — the spec forbids reintroducing it.
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
	// deliver it over virtiofs (not a 9p rw share of the host checkout): a
	// workspace holds only tracked files (no node_modules), lives on the
	// host (visible in `jj log`), and virtiofs gives the SQLite/flock
	// semantics the store index needs. Spec W1.
	work := filepath.Join(runDir, "work")
	wsName := "run-" + run.ID
	if err := materializeWorkspace(run.cwd, work, wsName); err != nil {
		run.failCrashed("vm launcher: materialize workspace: " + err.Error())
		return err
	}
	// Repoint the workspace's `.jj/repo` at the guest store mount so in-guest
	// jj reaches the host store through virtiofs (spec W2); the store itself
	// is the .jj/.git subtree mount below.
	if err := pointGuestJJStore(work); err != nil {
		run.failCrashed("vm launcher: point guest jj store: " + err.Error())
		return err
	}

	mounts := l.virtiofsMounts(runDir, work, run.cwd)

	// A workspace (and soon a qcow2) now exists on disk. Every NON-success
	// exit below — virtiofsd/console/boot failure, crash-before-terminal,
	// cancel — must (a) not leak the virtiofsd daemons and (b) leave a
	// vm.json so the reaper reclaims the work dir, qcow2, and jj workspace
	// on retention; without the record the reaper skips the dir and it leaks
	// forever (B2). Success flips `recorded` (writing live pids) and detaches
	// the daemons (they must stay up to serve the persisted VM's mounts).
	var vfsds []*exec.Cmd
	recorded := false
	// stopWatch ends the per-daemon watcher goroutines when Launch returns —
	// on success the daemons are handed to the reaper (still alive), and their
	// eventual reaper-kill must NOT be misreported as a mid-run death.
	stopWatch := make(chan struct{})
	vfsdDied := make(chan error, len(mounts))
	defer func() {
		close(stopWatch)
		for _, d := range vfsds {
			if d != nil && d.Process != nil {
				_ = d.Process.Kill()
			}
		}
		if !recorded {
			l.recordVM(vmRecord{RunID: run.ID, Dir: runDir, RepoDir: run.cwd, Workspace: wsName})
		}
	}()

	for _, m := range mounts {
		d, waitCh, err := l.startVirtiofsd(m.sock, m.hostDir, filepath.Join(runDir, "virtiofsd-"+m.tag+".log"))
		if err != nil {
			run.failCrashed("vm launcher: start virtiofsd: " + err.Error())
			return err
		}
		vfsds = append(vfsds, d)
		tag := m.tag
		go func() {
			select {
			case werr := <-waitCh:
				select {
				case vfsdDied <- fmt.Errorf("virtiofsd[%s] exited: %v", tag, werr):
				case <-stopWatch:
				}
			case <-stopWatch:
			}
		}()
	}

	cmd := exec.Command(runnerScript)
	cmd.Env = l.vmEnv(runDir, jobDir, work, virtiofsQemuOpts(mounts, l.memMiB))
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
			// The run finished (phoned home). Leave the VM — and its
			// virtiofsd daemons, which still serve the mounts — running for
			// inspection; record every pid for the reaper.
			pids := make([]int, len(vfsds))
			for i, d := range vfsds {
				pids[i] = d.Process.Pid
			}
			l.recordVM(vmRecord{RunID: run.ID, Pid: cmd.Process.Pid, VfsdPids: pids, Dir: runDir, RepoDir: run.cwd, Workspace: wsName})
			recorded = true // suppress the defer's failure record
			vfsds = nil     // hand the daemons off to the reaper (don't kill)
			return nil
		}
		select {
		case werr := <-exited:
			if !run.isTerminal() {
				run.failCrashed(fmt.Sprintf("vm exited before completion: %v (see %s/vm-console.log)", werr, runDir))
			}
			return werr
		case derr := <-vfsdDied:
			// A virtiofsd died: the guest mount it served is now dead, so the
			// run cannot make progress and qemu would otherwise idle forever.
			// Kill the VM and fail the run rather than blocking indefinitely.
			_ = cmd.Process.Kill()
			<-exited
			run.failCrashed(fmt.Sprintf("vm launcher: %v; guest mount lost (see %s/vm-console.log)", derr, runDir))
			return fmt.Errorf("vm launcher: %v", derr)
		case <-ticker.C:
			if run.IsCancelled() {
				_ = cmd.Process.Kill()
				<-exited
				return nil
			}
		}
	}
}

// startVirtiofsd launches a per-run virtiofsd serving sharedDir over sock
// (cache=never) and waits for the socket to appear so qemu can connect on
// boot. Its output goes to logPath. On success it returns the running process
// and a channel that delivers the daemon's exit — the caller watches it so a
// daemon dying mid-run (which would silently hang the guest mount) fails the
// run instead of blocking forever. cmd.Wait is called exactly once, in the
// goroutine feeding waitCh; the caller must not Wait again.
func (l vmLauncher) startVirtiofsd(sock, sharedDir, logPath string) (*exec.Cmd, <-chan error, error) {
	logf, err := os.Create(logPath)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(l.virtiofsdBin, virtiofsdArgs(sock, sharedDir)...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, nil, err
	}
	logf.Close() // the child inherited the fd at exec
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	// Wait for the listening socket before booting qemu — but bail
	// immediately if virtiofsd exits first (bad binary/args), rather than
	// blocking the whole timeout on a process that will never serve.
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			return cmd, waitCh, nil
		}
		select {
		case werr := <-waitCh:
			return nil, nil, fmt.Errorf("virtiofsd exited before creating socket %s: %v (see %s)", sock, werr, logPath)
		case <-timeout:
			_ = cmd.Process.Kill()
			return nil, nil, fmt.Errorf("virtiofsd socket %s did not appear (see %s)", sock, logPath)
		case <-ticker.C:
		}
	}
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

// vmRecord marks a persisted VM so the reaper can find and GC it. VfsdPids
// are the per-run virtiofsd daemons serving the mounts (workspace, jj store)
// — all killed alongside the qemu process; RepoDir + Workspace let the
// reaper `jj workspace forget` the per-run workspace before removing its dir
// (spec W1). VfsdPid is the legacy single-daemon field, still read so
// records written before the multi-mount change are reaped cleanly.
type vmRecord struct {
	RunID     string    `json:"run_id"`
	Pid       int       `json:"pid"`
	VfsdPids  []int     `json:"vfsd_pids,omitempty"`
	VfsdPid   int       `json:"vfsd_pid,omitempty"` // legacy single-daemon record
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
