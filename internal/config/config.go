// Package config loads Attractor's machine-local provider routing
// config (service-spec §1). The config maps a provider name to a
// backend mechanism (acp | claudecode | simulation), an agent command,
// and the env var used to pass llm_model to the agent. Codergen nodes
// declare intent (llm_provider / llm_model); the router resolves that
// intent to a backend through this config.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/allouis/attractor/internal/graph"
)

// Provider is one entry in the [providers.<name>] table.
type Provider struct {
	// Backend selects the mechanism: acp | claudecode | simulation.
	Backend string
	// Command is the agent command line (used by acp backends). Node and
	// graph acp_command attributes still take precedence over this.
	Command string
	// ModelEnv is the environment variable the agent reads its model
	// from; the router injects llm_model through it.
	ModelEnv string
}

// Config is the parsed provider routing config.
type Config struct {
	DefaultProvider string
	Providers       map[string]Provider
	// LinearAPIKey authenticates the Linear source (items-spec I2). The
	// daemon can't borrow the session's MCP, so the key lives in config.
	LinearAPIKey string
	// Checks maps a static-check name (deps, typecheck, lint, test) to the
	// shell command that runs it for this repo. A repo's own
	// .attractor/config.toml [checks] table supplies them; the daemon seeds
	// them into the run context as $context.check.<name> so a work pipeline
	// (implement, bug-fix) can gate on them without hardcoding a toolchain.
	Checks map[string]string
}

// Load reads ~/.attractor/config.toml then overlays
// ./.attractor/config.toml (cwd wins). Missing files are not an error:
// they yield an empty Config, which leaves routing to fall back to the
// default provider (or, absent that, the CLI's --backend override).
func Load(homeDir, cwd string) (Config, error) {
	cfg := Config{Providers: map[string]Provider{}}
	paths := []string{
		filepath.Join(homeDir, ".attractor", "config.toml"),
		filepath.Join(cwd, ".attractor", "config.toml"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Config{}, err
		}
		layer, err := Parse(data)
		if err != nil {
			return Config{}, err
		}
		cfg.overlay(layer)
	}
	// Environment fallback: LINEAR_API_KEY lets the Linear key live in the
	// process env (like GH_TOKEN does for the GitHub source via `gh`)
	// rather than only in config.toml. A config file still wins when both
	// are set.
	if cfg.LinearAPIKey == "" {
		if k := os.Getenv("LINEAR_API_KEY"); k != "" {
			cfg.LinearAPIKey = k
		}
	}
	return cfg, nil
}

// overlay merges src onto the receiver, src winning on any key it sets.
func (c *Config) overlay(src Config) {
	if src.DefaultProvider != "" {
		c.DefaultProvider = src.DefaultProvider
	}
	if src.LinearAPIKey != "" {
		c.LinearAPIKey = src.LinearAPIKey
	}
	if len(src.Checks) > 0 {
		if c.Checks == nil {
			c.Checks = map[string]string{}
		}
		for k, v := range src.Checks {
			c.Checks[k] = v
		}
	}
	if c.Providers == nil {
		c.Providers = map[string]Provider{}
	}
	for name, sp := range src.Providers {
		p := c.Providers[name]
		if sp.Backend != "" {
			p.Backend = sp.Backend
		}
		if sp.Command != "" {
			p.Command = sp.Command
		}
		if sp.ModelEnv != "" {
			p.ModelEnv = sp.ModelEnv
		}
		c.Providers[name] = p
	}
}

// InferProvider guesses the provider from an llm_model prefix
// (service-spec §1). Returns "" when the prefix is unrecognised.
func InferProvider(model string) string {
	switch {
	case strings.HasPrefix(model, "claude"):
		return "anthropic"
	case strings.HasPrefix(model, "gpt"), strings.HasPrefix(model, "o"):
		return "openai"
	case strings.HasPrefix(model, "gemini"):
		return "google"
	}
	return ""
}

// ResolveProvider picks the provider name for a codergen node: the
// explicit llm_provider attribute, else inferred from llm_model, else
// the config's default_provider.
func (c Config) ResolveProvider(n *graph.Node) string {
	if p := strings.TrimSpace(n.Attrs["llm_provider"]); p != "" {
		return p
	}
	if p := InferProvider(strings.TrimSpace(n.Attrs["llm_model"])); p != "" {
		return p
	}
	return c.DefaultProvider
}
