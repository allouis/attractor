package config

import (
	"fmt"
	"strings"
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
	for i, raw := range strings.Split(string(data), "\n") {
		line := stripComment(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, err := parseTableHeader(line)
			if err != nil {
				return Config{}, fmt.Errorf("config line %d: %w", i+1, err)
			}
			curProvider = name
			if _, ok := cfg.Providers[name]; !ok {
				cfg.Providers[name] = Provider{}
			}
			continue
		}
		key, val, err := parseKeyValue(line)
		if err != nil {
			return Config{}, fmt.Errorf("config line %d: %w", i+1, err)
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

// parseTableHeader extracts <name> from a `[providers.<name>]` header.
// Only the providers table is meaningful; other tables yield an empty
// scope so their keys are ignored.
func parseTableHeader(line string) (string, error) {
	if !strings.HasSuffix(line, "]") {
		return "", fmt.Errorf("malformed table header %q", line)
	}
	inner := strings.TrimSpace(line[1 : len(line)-1])
	if rest, ok := strings.CutPrefix(inner, "providers."); ok {
		name := strings.TrimSpace(rest)
		if name == "" {
			return "", fmt.Errorf("empty provider name in %q", line)
		}
		return name, nil
	}
	return "", nil // non-providers table: ignored scope
}

// parseKeyValue splits `key = "value"`, unquoting the value.
func parseKeyValue(line string) (key, val string, err error) {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return "", "", fmt.Errorf("expected key = value, got %q", line)
	}
	key = strings.TrimSpace(line[:eq])
	val = strings.TrimSpace(line[eq+1:])
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
		val = val[1 : len(val)-1]
	}
	return key, val, nil
}

// stripComment removes a trailing `#` comment that sits outside a
// quoted string and trims surrounding whitespace.
func stripComment(line string) string {
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		ch := line[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '#':
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}
