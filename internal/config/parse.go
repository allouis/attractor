package config

import (
	"fmt"
	"strings"

	"github.com/fabro/attractor/internal/toml"
)

// Parse reads the minimal TOML subset Attractor's config uses:
// `#` line comments, top-level `key = "value"` pairs, and
// `[providers.<name>]` table headers whose `backend`/`command`/
// `model_env` keys populate a Provider. Anything richer than this
// (arrays, nested tables, integers) is intentionally unsupported — the
// config surface is deliberately tiny (service-spec §1).
func Parse(data []byte) (Config, error) {
	cfg := Config{Providers: map[string]Provider{}}
	var curProvider string // name of the [providers.<name>] table in scope
	ignore := false        // inside a non-providers table: skip its keys
	for i, raw := range strings.Split(string(data), "\n") {
		line := toml.StripComment(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, err := parseProviderHeader(line)
			if err != nil {
				return Config{}, fmt.Errorf("config line %d: %w", i+1, err)
			}
			curProvider = name
			ignore = name == ""
			if !ignore {
				if _, ok := cfg.Providers[name]; !ok {
					cfg.Providers[name] = Provider{}
				}
			}
			continue
		}
		key, val, err := toml.ParseKeyValue(line)
		if err != nil {
			return Config{}, fmt.Errorf("config line %d: %w", i+1, err)
		}
		if ignore {
			continue
		}
		if curProvider == "" {
			if key == "default_provider" {
				cfg.DefaultProvider = val
			}
			continue
		}
		p := cfg.Providers[curProvider]
		switch key {
		case "backend":
			p.Backend = val
		case "command":
			p.Command = val
		case "model_env":
			p.ModelEnv = val
		}
		cfg.Providers[curProvider] = p
	}
	return cfg, nil
}

// parseProviderHeader extracts <name> from a `[providers.<name>]`
// header. Only the providers table is meaningful; other tables yield an
// empty scope so their keys are ignored.
func parseProviderHeader(line string) (string, error) {
	path, err := toml.TableHeader(line)
	if err != nil {
		return "", err
	}
	if len(path) >= 2 && path[0] == "providers" {
		// Join the remaining segments so a dotted provider name like
		// `[providers.foo.bar]` is preserved as "foo.bar" (regressed when
		// the header was split into segments; previously CutPrefix kept the
		// full remainder).
		return strings.Join(path[1:], "."), nil
	}
	return "", nil // non-providers table: ignored scope
}
