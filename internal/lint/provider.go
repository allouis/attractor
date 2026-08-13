package lint

import (
	"fmt"
	"strings"

	"github.com/allouis/attractor/internal/config"
	"github.com/allouis/attractor/internal/graph"
)

// These two rules are config-aware, so they are not part of BuiltIn():
// the CLI constructs them from the loaded provider config and passes
// them to Validate as extra rules (docs/provider-config.md).

// ProviderKnownRule warns when a codergen node resolves to a provider
// that the machine-local config does not define. It is a warning, not
// an error, because the config is machine-local: a pipeline may be run
// on a machine that does configure the provider.
type ProviderKnownRule struct {
	Config config.Config
}

func (ProviderKnownRule) Name() string { return "provider_known" }

func (r ProviderKnownRule) Apply(g *graph.Graph) []Diagnostic {
	var out []Diagnostic
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if !strings.HasPrefix(n.Type(), "codergen") {
			continue
		}
		name := r.Config.ResolveProvider(n)
		if name == "" {
			continue // no provider intent declared
		}
		if _, ok := r.Config.Providers[name]; ok {
			continue
		}
		out = append(out, Diagnostic{
			Rule:     "provider_known",
			Severity: Warning,
			NodeID:   id,
			Message:  fmt.Sprintf("node resolves to provider %q, which is absent from the config", name),
		})
	}
	return out
}

// ModelEnvMissingRule warns when a codergen node sets llm_model but its
// resolved provider defines no model_env, so the model has no way to
// reach the agent. Nodes whose provider is unconfigured are left to
// ProviderKnownRule.
type ModelEnvMissingRule struct {
	Config config.Config
}

func (ModelEnvMissingRule) Name() string { return "model_env_missing" }

func (r ModelEnvMissingRule) Apply(g *graph.Graph) []Diagnostic {
	var out []Diagnostic
	for _, id := range g.NodeOrder {
		n := g.Nodes[id]
		if !strings.HasPrefix(n.Type(), "codergen") {
			continue
		}
		if strings.TrimSpace(n.Attrs["llm_model"]) == "" {
			continue
		}
		name := r.Config.ResolveProvider(n)
		p, ok := r.Config.Providers[name]
		if !ok {
			continue // unconfigured provider: ProviderKnownRule reports it
		}
		if p.ModelEnv != "" {
			continue
		}
		out = append(out, Diagnostic{
			Rule:     "model_env_missing",
			Severity: Warning,
			NodeID:   id,
			Message:  fmt.Sprintf("node sets llm_model but provider %q has no model_env; the model will not reach the agent", name),
		})
	}
	return out
}
