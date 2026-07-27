package config

import (
	"fmt"
	"os"
	"path/filepath"
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

// LoadRepos reads ~/.attractor/repos.toml then overlays
// ./.attractor/repos.toml (cwd wins), mirroring Load. Missing files are
// not an error: they yield an empty map.
func LoadRepos(homeDir, cwd string) (Repos, error) {
	repos := Repos{}
	paths := []string{
		filepath.Join(homeDir, ".attractor", "repos.toml"),
		filepath.Join(cwd, ".attractor", "repos.toml"),
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		layer, err := ParseRepos(data)
		if err != nil {
			return nil, err
		}
		for name, path := range layer {
			repos[name] = path
		}
	}
	return repos, nil
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
