package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// chdir switches cwd to dir for the test and restores it on cleanup.
// (t.Chdir needs go 1.24; go.mod pins 1.23.)
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %q: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

// writeFile creates path (and parents) with trivial content.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("digraph d {}"), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func TestResolvePipelinePathEnvFallback(t *testing.T) {
	bundle := t.TempDir()
	writeFile(t, filepath.Join(bundle, "plan-build-review", "pipeline.dot"))
	writeFile(t, filepath.Join(bundle, "solo.dot"))
	t.Setenv("ATTRACTOR_PIPELINES", bundle)
	t.Setenv("HOME", t.TempDir())
	chdir(t, t.TempDir())

	for name, want := range map[string]string{
		"plan-build-review": filepath.Join(bundle, "plan-build-review", "pipeline.dot"),
		"solo":              filepath.Join(bundle, "solo.dot"),
	} {
		got, err := resolvePipelinePath(name)
		if err != nil {
			t.Fatalf("resolve %q: %v", name, err)
		}
		if got != want {
			t.Errorf("resolve %q = %q, want %q", name, got, want)
		}
	}
}

func TestResolvePipelinePathEnvUnset(t *testing.T) {
	bundle := t.TempDir()
	writeFile(t, filepath.Join(bundle, "solo.dot"))
	t.Setenv("ATTRACTOR_PIPELINES", "")
	t.Setenv("HOME", t.TempDir())
	chdir(t, t.TempDir())

	_, err := resolvePipelinePath("solo")
	if err == nil {
		t.Fatal("expected lookup to fail with env unset")
	}
	if strings.Contains(err.Error(), bundle) {
		t.Errorf("tried list leaked bundle path: %v", err)
	}
}

func TestResolvePipelinePathCwdBeatsEnv(t *testing.T) {
	bundle := t.TempDir()
	writeFile(t, filepath.Join(bundle, "solo.dot"))
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "pipelines", "solo.dot"))
	t.Setenv("ATTRACTOR_PIPELINES", bundle)
	t.Setenv("HOME", t.TempDir())
	chdir(t, cwd)

	got, err := resolvePipelinePath("solo")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(cwd, "pipelines", "solo.dot")
	if got != want {
		t.Errorf("resolve = %q, want cwd copy %q", got, want)
	}
}

func TestResolveStylesheetPathEnvFallback(t *testing.T) {
	bundle := t.TempDir()
	writeFile(t, filepath.Join(bundle, "models.css"))
	t.Setenv("ATTRACTOR_PIPELINES", bundle)
	chdir(t, t.TempDir())

	got, err := resolveStylesheetPath("models.css")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(bundle, "models.css"); got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

func TestResolveStylesheetPathCwdWins(t *testing.T) {
	bundle := t.TempDir()
	writeFile(t, filepath.Join(bundle, "models.css"))
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "models.css"))
	t.Setenv("ATTRACTOR_PIPELINES", bundle)
	chdir(t, cwd)

	got, err := resolveStylesheetPath("models.css")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "models.css" {
		t.Errorf("resolve = %q, want cwd-relative %q", got, "models.css")
	}
}

// The docs/README all use `--stylesheet pipelines/models.css`; the
// bundle root already ends in /pipelines, so the leading segment must be
// stripped to avoid a .../pipelines/pipelines/models.css double-join.
func TestResolveStylesheetPathBundlePrefix(t *testing.T) {
	bundle := t.TempDir()
	writeFile(t, filepath.Join(bundle, "models.css"))
	t.Setenv("ATTRACTOR_PIPELINES", bundle)
	chdir(t, t.TempDir())

	got, err := resolveStylesheetPath("pipelines/models.css")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := filepath.Join(bundle, "models.css"); got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
}

// A non-absence stat error (here ENOTDIR: a path component is a file)
// must be returned as-is, not remapped to a synthetic "not found", so
// the fallback stays reserved for genuine absence and callers keep
// errors.Is.
func TestResolveStylesheetPathPreservesNonAbsenceError(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "file"))
	t.Setenv("ATTRACTOR_PIPELINES", "")
	chdir(t, dir)

	_, err := resolveStylesheetPath("file/nope.css")
	if err == nil {
		t.Fatal("expected error stat-ing through a file component")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ENOTDIR misreported as absence: %v", err)
	}
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Errorf("underlying error not preserved, got %v", err)
	}
}

func TestResolveStylesheetPathMissing(t *testing.T) {
	bundle := t.TempDir()
	t.Setenv("ATTRACTOR_PIPELINES", bundle)
	chdir(t, t.TempDir())

	if _, err := resolveStylesheetPath("nope.css"); err == nil {
		t.Fatal("expected error when stylesheet is in neither cwd nor bundle")
	}

	t.Setenv("ATTRACTOR_PIPELINES", "")
	if _, err := resolveStylesheetPath("nope.css"); err == nil {
		t.Fatal("expected error when stylesheet missing and env unset")
	}
}
