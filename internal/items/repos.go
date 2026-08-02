package items

// Repos maps a GitHub `owner/name` to a local jj-colocated checkout
// (items-spec I3). A dispatched workflow owns its own checkout; the
// daemon resolves the item's `repo` var through this map to set the run's
// cwd (consumed in I4). The map is a projection of the daemon-owned
// config.json (config-screen-spec); config.Document.ReposMap builds it.
type Repos map[string]string

// Path returns the local checkout for an `owner/name`, ok=false if unset.
func (r Repos) Path(repo string) (string, bool) {
	p, ok := r[repo]
	return p, ok
}
