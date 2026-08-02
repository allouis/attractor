package config

import (
	"fmt"
	"os"
	"sort"
)

// validBackends is the set of provider mechanisms the router can route
// (backend/router/router.go). PUT /config rejects any other backend.
var validBackends = map[string]bool{"acp": true, "claudecode": true, "simulation": true}

// RedactedDocument is the config as returned by GET /config: identical to
// Document except the Linear secret is replaced by a set/unset flag. The
// hard rule is redact secrets on read — the key is never echoed.
type RedactedDocument struct {
	DefaultProvider string                `json:"default_provider"`
	Providers       map[string]Provider   `json:"providers"`
	Linear          RedactedLinear        `json:"linear"`
	Repos           map[string]RepoConfig `json:"repos"`
	// VMImages surfaces the named VM boot-image registry unredacted (image
	// name -> boot script): the Config Repos panel reads it to populate the
	// per-repo image select (per-repo VM config, VM4). Not a secret.
	VMImages map[string]string `json:"vm_images,omitempty"`
}

// RedactedLinear reports whether a Linear key is stored, never its value.
type RedactedLinear struct {
	APIKeySet bool `json:"api_key_set"`
}

// Redacted projects the document onto its read-safe form: everything but
// the Linear key verbatim, the key reduced to api_key_set.
func (d Document) Redacted() RedactedDocument {
	return RedactedDocument{
		DefaultProvider: d.DefaultProvider,
		Providers:       d.Providers,
		Linear:          RedactedLinear{APIKeySet: d.Linear.APIKey != ""},
		Repos:           d.Repos,
		VMImages:        d.VMImages,
	}
}

// Validate enforces the config-screen-spec's "reject structural, warn
// soft" rule. It returns a structural error (the whole PUT fails, nothing
// saved) for an unknown provider backend or a dangling default_provider,
// and soft warnings (the document still saves) for repo paths that don't
// resolve to a directory. Secrets and shell commands are unvalidated.
func (d Document) Validate() (warnings []string, err error) {
	for name, p := range d.Providers {
		if !validBackends[p.Backend] {
			return nil, fmt.Errorf("provider %q has unknown backend %q (want acp|claudecode|simulation)", name, p.Backend)
		}
	}
	if d.DefaultProvider != "" {
		if _, ok := d.Providers[d.DefaultProvider]; !ok {
			return nil, fmt.Errorf("default_provider %q names no configured provider", d.DefaultProvider)
		}
	}
	for _, name := range sortedRepoNames(d.Repos) {
		path := d.Repos[name].Path
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("repo %q: path %q does not resolve to a directory", name, path))
		}
	}
	return warnings, nil
}

// WithMergedSecrets implements the PUT secret-merge: an empty incoming
// secret keeps the stored value, a non-empty one replaces it, and an
// explicit ClearAPIKey drops it (the distinct clear action — an empty key
// alone always preserves). d is the incoming (UI-submitted) document, prev
// the currently stored one. The clear signal is consumed here, never
// persisted.
func (d Document) WithMergedSecrets(prev Document) Document {
	switch {
	case d.Linear.ClearAPIKey:
		d.Linear.APIKey = ""
	case d.Linear.APIKey == "":
		d.Linear.APIKey = prev.Linear.APIKey
	}
	d.Linear.ClearAPIKey = false
	return d
}

// RepoRef is one entry in the GET /repos projection (run-workflow-spec
// R2): a registered repo's owner/name and its local checkout.
type RepoRef struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ReposList projects the registered repos onto a name-sorted slice — the
// stable order the run-form repo dropdown reads.
func (d Document) ReposList() []RepoRef {
	names := sortedRepoNames(d.Repos)
	list := make([]RepoRef, 0, len(names))
	for _, name := range names {
		list = append(list, RepoRef{Name: name, Path: d.Repos[name].Path})
	}
	return list
}

func sortedRepoNames(repos map[string]RepoConfig) []string {
	names := make([]string, 0, len(repos))
	for name := range repos {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
