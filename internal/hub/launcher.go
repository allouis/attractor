package hub

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// Launcher lets the hub start runs (plan Phase 4: hub = directory +
// scraper + launcher). Each launched run is an independent
// `attractor run --announce` subprocess serving its own state; the hub
// only records where it is and scrapes.
type Launcher struct {
	// Bin is the attractor binary to spawn. Empty resolves the running
	// executable.
	Bin string
	// HubURL is the base URL runs announce back to.
	HubURL string
	// LogsBase is where each run's logs dir is created.
	LogsBase string
}

// launchBody is the POST /pipelines submission.
type launchBody struct {
	// Path is a pipeline name or .dot path resolvable by `attractor run`.
	Path string            `json:"path"`
	Cwd  string            `json:"cwd,omitempty"`
	Vars map[string]string `json:"vars,omitempty"`
}

// LaunchArgs builds the child argv. Pure for testability; deterministic
// -var order.
func (l Launcher) LaunchArgs(body launchBody, logsDir string) []string {
	args := []string{"run", "--announce", l.HubURL, "--logs", logsDir}
	if body.Cwd != "" {
		args = append(args, "--cwd", body.Cwd)
	}
	keys := make([]string, 0, len(body.Vars))
	for k := range body.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, "-var", k+"="+body.Vars[k])
	}
	return append(args, body.Path)
}

// handleLaunch spawns the run detached: the hub is not its supervisor —
// the run owns itself and the hub finds out everything by scraping.
func (l Launcher) handleLaunch(w http.ResponseWriter, r *http.Request) {
	var body launchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		http.Error(w, "want {path, cwd?, vars?}", http.StatusBadRequest)
		return
	}
	bin := l.Bin
	if bin == "" {
		exe, err := os.Executable()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		bin = exe
	}
	logsDir, err := os.MkdirTemp(l.LogsBase, "attractor-run-")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cmd := exec.Command(bin, l.LaunchArgs(body, logsDir)...)
	logFile, err := os.Create(filepath.Join(logsDir, "launch.log"))
	if err == nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go func() {
		_ = cmd.Wait()
		if logFile != nil {
			_ = logFile.Close()
		}
	}()
	writeJSON(w, map[string]string{"logs": logsDir})
}
