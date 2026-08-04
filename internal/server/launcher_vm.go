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
}

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

// vmEnv builds the environment run-nixos-vm reads: the per-run qcow2 disk
// and the share sources the generated boot script expands into its qemu
// args (ATTRACTOR_JOB_DIR over 9p, ATTRACTOR_WORKSPACE — the mutable
// working copy).
func (l vmLauncher) vmEnv(runDir, jobDir, workspace string) []string {
	return append(os.Environ(),
		"NIX_DISK_IMAGE="+filepath.Join(runDir, "vm.qcow2"),
		"ATTRACTOR_JOB_DIR="+jobDir,
		"ATTRACTOR_WORKSPACE="+workspace,
	)
}

// materializeWorkspace adds an isolated jj workspace of repoDir at dest,
// named name (spec W1). jj workspaces materialise only TRACKED files, so a
// gitignored node_modules never appears — the run installs its own,
// matching its own lockfile, and its mutations never touch the host
// checkout. The workspace lives on the host and shows in `jj log`, the
// property virtiofs delivery preserves.
func materializeWorkspace(repoDir, dest, name string) error {
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
	runDir := filepath.Join(l.vmDir, run.ID)
	jobDir, err := l.writeJob(run, reportURL)
	if err != nil {
		run.failCrashed("vm launcher: prepare job: " + err.Error())
		return err
	}

	cmd := exec.Command(runnerScript)
	cmd.Env = l.vmEnv(runDir, jobDir, run.cwd)
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
			// The run finished (phoned home). Leave the VM running so it
			// persists for inspection; record it for the reaper.
			l.recordVM(run.ID, runDir, cmd.Process.Pid)
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

// vmRecord marks a persisted VM so the reaper can find and GC it.
type vmRecord struct {
	RunID     string    `json:"run_id"`
	Pid       int       `json:"pid"`
	Dir       string    `json:"dir"`
	StartedAt time.Time `json:"started_at"`
}

// recordVM writes the marker the reaper uses to GC persisted VMs.
func (l vmLauncher) recordVM(runID, runDir string, pid int) {
	rec := vmRecord{RunID: runID, Pid: pid, Dir: runDir, StartedAt: time.Now()}
	data, _ := json.MarshalIndent(rec, "", "  ")
	_ = os.WriteFile(filepath.Join(runDir, "vm.json"), data, 0o644)
}
