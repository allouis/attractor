package attractor_test

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoRawFilesystemWritesInCore is the structural guard behind the
// runstore seam (layer 1): the execution core must not call raw
// filesystem-write or ambient-cwd primitives directly. Every artifact
// write goes through internal/runstore (a rooted capability that cannot
// write outside its directory), so a codergen/tool/backend node can never
// write into the process working directory / a user's repo — the class of
// bug this guard makes impossible.
//
// A genuinely-needed exception (e.g. reading the ambient cwd to *locate* a
// subprocess's leaked file) must annotate its line with a trailing
// `// runstore:allow <reason>` comment, which documents why it is safe.
func TestNoRawFilesystemWritesInCore(t *testing.T) {
	root := repoRootForGuard(t)

	// Packages where the runstore seam is the only sanctioned write path.
	coreDirs := []string{
		filepath.Join("internal", "engine"),
		filepath.Join("internal", "handler"),
		filepath.Join("internal", "backend"),
	}
	banned := regexp.MustCompile(`\bos\.(WriteFile|Create|OpenFile|MkdirAll|Chdir|Getwd)\b`)

	var violations []string
	for _, dir := range coreDirs {
		abs := filepath.Join(root, dir)
		err := filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1024*1024), 1024*1024)
			ln := 0
			for sc.Scan() {
				ln++
				line := sc.Text()
				if banned.MatchString(line) && !strings.Contains(line, "runstore:allow") {
					rel, _ := filepath.Rel(root, path)
					violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, ln, strings.TrimSpace(line)))
				}
			}
			return sc.Err()
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("raw filesystem/cwd calls in the execution core — route them through internal/runstore, "+
			"or annotate the line with `// runstore:allow <reason>` if genuinely safe:\n%s",
			strings.Join(violations, "\n"))
	}
}

// repoRootForGuard walks up from the test's working directory to the module
// root (the directory containing go.mod).
func repoRootForGuard(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found from test working directory")
		}
		dir = parent
	}
}
