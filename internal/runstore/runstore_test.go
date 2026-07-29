package runstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDir_WriteReadWithinRoot(t *testing.T) {
	root := t.TempDir()
	d := New(root)
	if err := d.Write("a/b.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	got, err := d.Read("a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi" {
		t.Fatalf("got %q", got)
	}
	// The file must live under root, nowhere else.
	if _, err := os.Stat(filepath.Join(root, "a", "b.txt")); err != nil {
		t.Fatalf("file not under root: %v", err)
	}
}

// TestDir_RejectsEscapes is the core invariant: a Dir can never write
// outside its root, so it cannot touch the process cwd / a user's repo.
func TestDir_RejectsEscapes(t *testing.T) {
	root := t.TempDir()
	d := New(root)
	for _, name := range []string{
		"../escape.txt",
		"../../escape.txt",
		"a/../../escape.txt",
		"/etc/passwd",
		filepath.Join(t.TempDir(), "abs.txt"),
	} {
		if err := d.Write(name, []byte("x")); err == nil {
			t.Fatalf("Write(%q) should be rejected", name)
		}
		if _, err := d.Path(name); err == nil {
			t.Fatalf("Path(%q) should be rejected", name)
		}
	}
	// Nothing must have escaped: the parent of root stays untouched.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("an escape wrote outside root")
	}
}

func TestDir_SubIsRootedUnderParent(t *testing.T) {
	root := t.TempDir()
	d := New(root).Sub("stage", "child")
	want := filepath.Join(root, "stage", "child")
	if d.Root() != want {
		t.Fatalf("Sub root = %q, want %q", d.Root(), want)
	}
	if err := d.Write("status.json", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(want, "status.json")); err != nil {
		t.Fatalf("sub write not under expected root: %v", err)
	}
}

func TestDir_RemoveAndExists(t *testing.T) {
	d := New(t.TempDir())
	if d.Exists("x") {
		t.Fatal("should not exist yet")
	}
	must(t, d.Write("x", []byte("1")))
	if !d.Exists("x") {
		t.Fatal("should exist")
	}
	must(t, d.Remove("x"))
	if d.Exists("x") {
		t.Fatal("should be gone")
	}
	// Remove of an absent file is not an error.
	must(t, d.Remove("x"))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
