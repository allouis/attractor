// Package config loads Attractor's machine-local provider routing
// config (service-spec §1). The config maps a provider name to a
// backend mechanism (acp | claudecode | simulation), an agent command,
// and the env var used to pass llm_model to the agent. Codergen nodes
// declare intent (llm_provider / llm_model); the router resolves that
// intent to a backend through this config.
package config

import (
	"strings"

	"github.com/allouis/attractor/internal/graph"
)

// Provider is one entry in the config's providers map.
type Provider struct {
	// Backend selects the mechanism: acp | claudecode | simulation.
	Backend string `json:"backend"`
	// Command is the agent command line (used by acp backends). Node and
	// graph acp_command attributes still take precedence over this.
	Command string `json:"command"`
	// ModelEnv is the environment variable the agent reads its model
	// from; the router injects llm_model through it.
	ModelEnv string `json:"model_env"`
}

// Config is the parsed provider routing config.
type Config struct {
	DefaultProvider string
	Providers       map[string]Provider
	// LinearAPIKey authenticates the Linear source (items-spec I2). The
	// daemon can't borrow the session's MCP, so the key lives in config.
	LinearAPIKey string
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
