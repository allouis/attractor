// Package version exposes the build's git revision so the daemon can warn
// when a phone-home child (subprocess / VM image) is running a different
// build than itself (docs/nix-vm-runner-spec.md — image/daemon skew).
package version

import "runtime/debug"

// RevisionHeader carries a phone-home child's git revision so the daemon
// can detect skew against its own build.
const RevisionHeader = "X-Attractor-Revision"

// Number is the human-facing release version, bumped by hand at release
// time. Distinct from Revision, which is the exact build's git commit.
const Number = "0.1.0"

// Revision is the git revision, injected at build time via
// `-ldflags "-X …/internal/version.Revision=<rev>"` (the flake sets it from
// self.rev). Empty for a plain `go build`, which then falls back to the VCS
// revision the Go toolchain embeds from the source git tree.
var Revision string

// Get returns the build's git revision as a 12-char prefix, or "unknown"
// when neither the ldflags stamp nor the toolchain's VCS metadata is
// available. Normalizing to a prefix lets a nix-built binary (full 40-char
// self.rev) and a `go build` binary (VCS revision) compare equal for the
// same commit.
func Get() string {
	rev := Revision
	if rev == "" {
		rev = vcsRevision()
	}
	if rev == "" {
		return "unknown"
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev
}

func vcsRevision() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
}
