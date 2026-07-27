package config

import (
	"fmt"
	"strings"

	"github.com/fabro/attractor/internal/toml"
)

// Repos maps a GitHub `owner/name` to a local jj-colocated checkout
// (items-spec I3). A dispatched workflow owns its own checkout; the
// daemon resolves the item's `repo` var through this map to set the run's
// cwd (consumed in I4).
type Repos map[string]string

// Path returns the local checkout for an `owner/name`, ok=false if unset.
func (r Repos) Path(repo string) (string, bool) {
	p, ok := r[repo]
	return p, ok
}

// ParseRepos reads a repos.toml: a single `[repos]` table whose keys are
// `owner/name` (quoted or bare — the `/` is fine in Attractor's tiny TOML
// subset) and whose values are local paths. Lines outside the table are
// ignored, matching Parse's deliberately small config surface.
func ParseRepos(data []byte) (Repos, error) {
	repos := Repos{}
	var curTable string
	for i, raw := range strings.Split(string(data), "\n") {
		line := toml.StripComment(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") {
			path, err := toml.TableHeader(line)
			if err != nil {
				return nil, fmt.Errorf("repos line %d: %w", i+1, err)
			}
			curTable = path[0]
			continue
		}
		key, val, err := toml.ParseKeyValue(line)
		if err != nil {
			return nil, fmt.Errorf("repos line %d: %w", i+1, err)
		}
		if curTable != "repos" {
			continue
		}
		repos[unquote(key)] = val
	}
	return repos, nil
}

// unquote strips a single layer of matching surrounding quotes; toml only
// unquotes values, but a repos key like "owner/name" is naturally quoted.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}
