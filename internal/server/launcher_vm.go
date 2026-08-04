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

// virtiofsdArgs builds the per-run virtiofsd invocation: serve sharedDir
// over a unix socket with cache=none — the strongest coherence + locking
// mode, required for the SQLite store index and instant host visibility
// (spec: virtiofsd cache mode; W2 may relax to auto if the lock test holds).
func virtiofsdArgs(sock, sharedDir string) []string {
	return []string{
		"--socket-path=" + sock,
		"--shared-dir=" + sharedDir,
		"--cache=none",
	}
}

// virtiofsQemuOpts builds the QEMU_OPTS run-nixos-vm appends to boot with a
// vhost-user-fs device backed by virtiofsd's socket, exported as tag. The
// device requires a shared-memory backend, so we add memory-backend-memfd
// (share=on) sized to the guest RAM and select it as the machine's memory.
// The guest mounts tag rw (nix/vm-runner.nix). Spec W1.
func virtiofsQemuOpts(sock, tag string, memMiB int) string {
	id := "vfs-" + tag
	return strings.Join([]string{
		fmt.Sprintf("-chardev socket,id=%s,path=%s", id, sock),
		fmt.Sprintf("-device vhost-user-fs-pci,chardev=%s,tag=%s", id, tag),
		fmt.Sprintf("-object memory-backend-memfd,id=mem,size=%dM,share=on", memMiB),
		"-machine memory-backend=mem",
	}, " ")
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

	// A workspace (and soon a qcow2) now exists on disk. Every NON-success
	// exit below — virtiofsd/console/boot failure, crash-before-terminal,
	// cancel — must (a) not leak the virtiofsd daemon and (b) leave a
	// vm.json so the reaper reclaims the work dir, qcow2, and jj workspace
	// on retention; without the record the reaper skips the dir and it leaks
	// forever (B2). Success flips `recorded` (writing live pids) and detaches
	// vfsd (the daemon must stay up to serve the persisted VM's mount).
	var vfsd *exec.Cmd
	recorded := false
	defer func() {
		if vfsd != nil && vfsd.Process != nil {
			_ = vfsd.Process.Kill()
		}
		if !recorded {
			l.recordVM(vmRecord{RunID: run.ID, Dir: runDir, RepoDir: run.cwd, Workspace: wsName})
		}
	}()

	sock := filepath.Join(runDir, "vfs.sock")
	vfsd, err = l.startVirtiofsd(sock, work, filepath.Join(runDir, "virtiofsd.log"))
	if err != nil {
		run.failCrashed("vm launcher: start virtiofsd: " + err.Error())
		return err
	}

	cmd := exec.Command(runnerScript)
	cmd.Env = l.vmEnv(runDir, jobDir, work, virtiofsQemuOpts(sock, "workspace", l.memMiB))
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
			// virtiofsd, which still serves the mount — running for
			// inspection; record both pids for the reaper.
			l.recordVM(vmRecord{RunID: run.ID, Pid: cmd.Process.Pid, VfsdPid: vfsd.Process.Pid, Dir: runDir, RepoDir: run.cwd, Workspace: wsName})
			recorded = true // suppress the defer's failure record
			vfsd = nil      // hand the daemon off to the reaper (don't kill)
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

// startVirtiofsd launches a per-run virtiofsd serving sharedDir over sock
// (cache=none) and waits for the socket to appear so qemu can connect on
// boot. Its output goes to logPath. Returns the running process.
func (l vmLauncher) startVirtiofsd(sock, sharedDir, logPath string) (*exec.Cmd, error) {
	logf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(l.virtiofsdBin, virtiofsdArgs(sock, sharedDir)...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, err
	}
	logf.Close() // the child inherited the fd at exec
	// Wait for the listening socket before booting qemu — but bail
	// immediately if virtiofsd exits first (bad binary/args), rather than
	// blocking the whole timeout on a process that will never serve.
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			return cmd, nil
		}
		select {
		case <-exited:
			return nil, fmt.Errorf("virtiofsd exited before creating socket %s (see %s)", sock, logPath)
		case <-timeout:
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("virtiofsd socket %s did not appear (see %s)", sock, logPath)
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

// vmRecord marks a persisted VM so the reaper can find and GC it. VfsdPid
// is the per-run virtiofsd serving the workspace mount (killed alongside
// the qemu process); RepoDir + Workspace let the reaper `jj workspace
// forget` the per-run workspace before removing its dir (spec W1).
type vmRecord struct {
	RunID     string    `json:"run_id"`
	Pid       int       `json:"pid"`
	VfsdPid   int       `json:"vfsd_pid,omitempty"`
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
