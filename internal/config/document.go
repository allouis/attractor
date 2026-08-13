package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Document is the whole user-owned config: one ~/.attractor/config.json.
// After the daemon strip it carries only what `attractor run` needs —
// the provider registry the backend router routes codergen nodes with.
type Document struct {
	DefaultProvider string              `json:"default_provider"`
	Providers       map[string]Provider `json:"providers"`
}

// ProviderConfig projects the document onto the config.Config the
// backend router consumes.
func (d Document) ProviderConfig() Config {
	cfg := Config{
		DefaultProvider: d.DefaultProvider,
		Providers:       d.Providers,
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]Provider{}
	}
	return cfg
}

// configPath is the config file under a home directory.
func configPath(homeDir string) string {
	return filepath.Join(homeDir, ".attractor", "config.json")
}

// DefaultDocument is the fresh config on first run: a ready-to-edit
// anthropic provider entry. default_provider is deliberately left unset
// so a fresh, agent-less install still simulates bare runs (router.go:
// a bare run with no provider selected falls back to simulation) rather
// than spawning an agent the user has not installed.
func DefaultDocument() Document {
	return Document{
		Providers: map[string]Provider{
			"anthropic": {Backend: "acp", Command: "claude-agent-acp", ModelEnv: "ANTHROPIC_MODEL"},
		},
	}
}

// LoadDocument reads ~/.attractor/config.json. A missing file is not an
// error: it yields DefaultDocument. A file that fails to parse surfaces
// the error rather than masking data loss. Unknown keys (from richer
// historical configs) are ignored by the JSON decoder, so old files
// still load.
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

// Save writes the document to ~/.attractor/config.json atomically (write
// a sibling temp file, then rename), owner-only in an owner-only dir.
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
