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
	if err := os.WriteFile(filepath.Join(jobDir, "source.dot"), []byte(run.Source()), 0o644); err != nil {
		return "", err
	}
	return jobDir, nil
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
	cmd.Env = append(os.Environ(),
		"NIX_DISK_IMAGE="+filepath.Join(runDir, "vm.qcow2"),
		"ATTRACTOR_JOB_DIR="+jobDir,
		"ATTRACTOR_WORKSPACE="+run.cwd,
	)
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
