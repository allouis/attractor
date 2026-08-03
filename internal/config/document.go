package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ProviderConfig projects the document onto the legacy config.Config the
// router and Linear source consume: default provider, providers, and the
// Linear key. LINEAR_API_KEY fills the key when the document sets none
// (the document wins when both are present), mirroring the retired
// config.toml env fallback (items-spec I2).
func (d Document) ProviderConfig() Config {
	cfg := Config{
		DefaultProvider: d.DefaultProvider,
		Providers:       d.Providers,
		LinearAPIKey:    d.Linear.APIKey,
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	if cfg.LinearAPIKey == "" {
		cfg.LinearAPIKey = os.Getenv("LINEAR_API_KEY")
	}
	return cfg
}

// ReposMap projects the registered repos onto an owner/name → path map,
// backing the daemon's repo → cwd resolution (items-spec I3).
func (d Document) ReposMap() map[string]string {
	repos := make(map[string]string, len(d.Repos))
	for name, rc := range d.Repos {
		repos[name] = rc.Path
	}
	return repos
}

// RepoForPath returns the owner/name of the registered repo whose local
// checkout is cwd, ok=false if none (or cwd is empty). It reverse-resolves
// a cwd-only dispatch (an automation or a raw-dot run, neither of which
// carries a repo ref) to the repo identity that keys per-repo checks, so
// those runs keep the checks they gated on before C3 (config-screen-spec).
func (d Document) RepoForPath(cwd string) (string, bool) {
	if cwd == "" {
		return "", false
	}
	for name, rc := range d.Repos {
		if rc.Path == cwd {
			return name, true
		}
	}
	return "", false
}

// RepoPath returns the local checkout path of the repo named by the given
// owner/name ref, ok=false when the ref is empty, unregistered, or maps to an
// empty path. It is the live-config counterpart of items.Repos.Path: the run
// form resolves cwd from the same document it reads the dropdown from, so a
// repo registered at runtime is immediately runnable (run-workflow-spec R3).
func (d Document) RepoPath(repo string) (string, bool) {
	if repo == "" {
		return "", false
	}
	rc, ok := d.Repos[repo]
	if !ok || rc.Path == "" {
		return "", false
	}
	return rc.Path, true
}

// ChecksForRepo returns the static-check commands of the repo named by the
// run's ref (owner/name), or nil when the ref is empty or unregistered.
// Checks are keyed by repo identity, not the run's cwd (config-screen-spec
// C3).
func (d Document) ChecksForRepo(repo string) map[string]string {
	return d.Repos[repo].Checks
}

// RepoRunner returns the launcher placement (direct | local | vm) the repo
// named by the run's ref declares, or "" when the ref is empty, unregistered,
// or declares none — leaving the daemon --runner default to apply at dispatch
// (per-repo VM config, VM2). Dispatch precedence (submission > repo > default)
// is layered on top in VM3.
func (d Document) RepoRunner(repo string) string {
	return d.Repos[repo].Runner
}

// RepoImage returns the vm_images name the repo's VM runs boot from, or ""
// when the ref is empty, unregistered, or declares no vm.image — leaving the
// "default" image to apply at dispatch. Only meaningful when RepoRunner is
// "vm" (per-repo VM config, VM2).
func (d Document) RepoImage(repo string) string {
	if vm := d.Repos[repo].VM; vm != nil {
		return vm.Image
	}
	return ""
}

// Document is the whole daemon-owned config (config-screen-spec): one
// ~/.attractor/config.json the daemon reads and writes. The legacy
// config.Config and items.Repos are projections of this single document.
type Document struct {
	DefaultProvider string                `json:"default_provider"`
	Providers       map[string]Provider   `json:"providers"`
	Linear          LinearConfig          `json:"linear"`
	Repos           map[string]RepoConfig `json:"repos"`
	// VMImages is the named VM boot-image registry: image name ->
	// run-nixos-vm boot script / flake ref. A repo's vm.image references a
	// name; "default" is the daemon's fallback image (per-repo VM config,
	// VM1). Omitted when empty so existing config files are unchanged.
	VMImages map[string]string `json:"vm_images,omitempty"`
}

// LinearConfig holds the Linear source's API key (items-spec I2). Stored
// here, redacted on read by the config API (config-screen-spec).
//
// ClearAPIKey is a write-only signal, never stored: because the redacted UI
// form always submits an empty key (the value is never echoed), secret-
// merge would keep the stored key forever. An explicit clear sets this so
// WithMergedSecrets drops the stored key instead of preserving it. It is
// omitempty + reset on merge so it never reaches the persisted file.
type LinearConfig struct {
	APIKey      string `json:"api_key"`
	ClearAPIKey bool   `json:"api_key_clear,omitempty"`
}

// RepoConfig is one registered repo: its local checkout plus the per-repo
// static-check commands. Checks moved from a flat config.toml [checks]
// table to nested-under-repo so the daemon can key them by the run's repo.
type RepoConfig struct {
	Path   string            `json:"path"`
	Checks map[string]string `json:"checks"`
	// Runner is the repo's declared launcher placement (direct | local | vm):
	// the dev environment every dispatch against this repo lands in, still
	// overridable per run (per-repo VM config, VM2). Empty leaves the daemon
	// --runner default. omitempty so repos that declare none stay unchanged.
	Runner string `json:"runner,omitempty"`
	// VM carries the repo's VM settings, only meaningful when Runner=="vm".
	// A pointer so an undeclared VM omits the key entirely, keeping existing
	// config files byte-unchanged (mirrors the vm_images omitempty precedent).
	VM *VMConfig `json:"vm,omitempty"`
}

// VMConfig is a repo's VM-launcher settings: the named vm_images entry its
// runs boot from. Empty Image resolves to the daemon's "default" image at
// dispatch (per-repo VM config, VM2).
type VMConfig struct {
	Image string `json:"image,omitempty"`
}

// configPath is the daemon-owned config file under a home directory.
func configPath(homeDir string) string {
	return filepath.Join(homeDir, ".attractor", "config.json")
}

// DefaultDocument is the fresh config on first run (no migration from the
// old TOML files): a ready-to-edit anthropic provider entry the UI can
// adopt, and empty maps the UI fills in. default_provider is deliberately
// left unset so a fresh, agent-less install still simulates bare runs
// (router.go: a bare run with no provider selected falls back to
// simulation) rather than spawning an agent the user has not installed.
func DefaultDocument() Document {
	return Document{
		Providers: map[string]Provider{
			"anthropic": {Backend: "acp", Command: "claude-agent-acp", ModelEnv: "ANTHROPIC_MODEL"},
		},
		Repos: map[string]RepoConfig{},
	}
}

// LoadDocument reads ~/.attractor/config.json. A missing file is not an
// error: it yields DefaultDocument (fresh default, no migration). A file
// that fails to parse surfaces the error rather than masking data loss.
func LoadDocument(homeDir string) (Document, error) {
	data, err := os.ReadFile(configPath(homeDir))
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultDocument(), nil
		}
		return Document{}, err
	}
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

// Save writes the document to ~/.attractor/config.json atomically (write a
// sibling temp file, then rename). The file carries secrets (the Linear
// key), so it is owner-only (0o600) in an owner-only dir.
func (d Document) Save(homeDir string) error {
	dir := filepath.Join(homeDir, ".attractor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "config-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, configPath(homeDir))
}
