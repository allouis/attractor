// Package runstore is the single seam through which Attractor writes run
// artifacts. A Dir is a capability handle to one directory that owns every
// write beneath it: names are always resolved *within* the root, and an
// absolute name or one that escapes the root via ".." is rejected. Code
// that holds a Dir therefore cannot write outside it — in particular it can
// never touch the process working directory or a user's repo, the class of
// bug this package exists to make impossible.
//
// Pass run/stage stores as capabilities (engine → HandlerEnv.Stage) instead
// of recomputing paths from a raw logs-root string. The `no-raw-fs` guard
// test (tests/guard_fswrite_test.go) forbids raw os write/cwd calls in the
// execution core so this seam stays the only way through.
package runstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Dir owns all writes beneath a single root directory.
type Dir struct{ root string }

// New returns a Dir rooted at root. A nil-safe zero value is not supported;
// callers with no on-disk root should not construct a Dir.
func New(root string) *Dir { return &Dir{root: filepath.Clean(root)} }

// Root returns the absolute (cleaned) root directory.
func (d *Dir) Root() string { return d.root }

// Sub returns a Dir rooted at a subdirectory of d (e.g. a per-stage dir
// under the run dir, or a child-run dir under a stage). Elements are joined
// beneath the root and cannot escape it.
func (d *Dir) Sub(elem ...string) *Dir {
	parts := append([]string{d.root}, elem...)
	return &Dir{root: filepath.Join(parts...)}
}

// resolve returns the absolute path for name or an error if name is
// absolute or escapes the root.
func (d *Dir) resolve(name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("runstore: absolute path %q not allowed", name)
	}
	p := filepath.Join(d.root, name)
	rel, err := filepath.Rel(d.root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("runstore: path %q escapes root %q", name, d.root)
	}
	return p, nil
}

// Path returns the rooted absolute path for name (for callers that must
// hand a path to another API, e.g. an event artifact link). It errors if
// name would escape the root.
func (d *Dir) Path(name string) (string, error) { return d.resolve(name) }

// MkdirAll creates the root directory (and parents).
func (d *Dir) MkdirAll() error {
	return os.MkdirAll(d.root, 0o755) // runstore:allow the seam owns dir creation
}

// Write writes data to name within the root, creating parent dirs. name may
// contain subdirectories (e.g. "tool_calls/1.json"); it may not escape.
func (d *Dir) Write(name string, data []byte) error {
	p, err := d.resolve(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // runstore:allow the seam owns dir creation
		return err
	}
	return os.WriteFile(p, data, 0o644) // runstore:allow the seam owns writes
}

// Read reads name within the root.
func (d *Dir) Read(name string) ([]byte, error) {
	p, err := d.resolve(name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Remove deletes name within the root (no error if absent).
func (d *Dir) Remove(name string) error {
	p, err := d.resolve(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) { // runstore:allow the seam owns removal
		return err
	}
	return nil
}

// Exists reports whether name exists within the root.
func (d *Dir) Exists(name string) bool {
	p, err := d.resolve(name)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// OpenAppend opens name within the root for appending, creating it and its
// parents. The caller closes the returned file.
func (d *Dir) OpenAppend(name string) (*os.File, error) {
	p, err := d.resolve(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // runstore:allow the seam owns dir creation
		return nil, err
	}
	return os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) // runstore:allow the seam owns writes
}
