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

// ChecksForRepo returns the static-check commands of the repo named by the
// run's ref (owner/name), or nil when the ref is empty or unregistered.
// Checks are keyed by repo identity, not the run's cwd (config-screen-spec
// C3).
func (d Document) ChecksForRepo(repo string) map[string]string {
	return d.Repos[repo].Checks
}

// Document is the whole daemon-owned config (config-screen-spec): one
// ~/.attractor/config.json the daemon reads and writes. The legacy
// config.Config and items.Repos are projections of this single document.
type Document struct {
	DefaultProvider string                `json:"default_provider"`
	Providers       map[string]Provider   `json:"providers"`
	Linear          LinearConfig          `json:"linear"`
	Repos           map[string]RepoConfig `json:"repos"`
}

// LinearConfig holds the Linear source's API key (items-spec I2). Stored
// here, redacted on read by the config API (config-screen-spec).
type LinearConfig struct {
	APIKey string `json:"api_key"`
}

// RepoConfig is one registered repo: its local checkout plus the per-repo
// static-check commands. Checks moved from a flat config.toml [checks]
// table to nested-under-repo so the daemon can key them by the run's repo.
type RepoConfig struct {
	Path   string            `json:"path"`
	Checks map[string]string `json:"checks"`
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
