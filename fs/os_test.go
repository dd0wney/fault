package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

// OS() is the passthrough that ships, and a passthrough that swallows an error
// is worse than no passthrough: the caller is told an operation succeeded.
//
// It also returns a nil File interface on failure rather than a non-nil
// interface holding a nil pointer, which would make a caller's "if f != nil"
// lie.
func TestOSReportsRealErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-directory", "f")
	fsys := faultfs.OS()

	f, err := fsys.OpenFile(missing, os.O_RDONLY, 0)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("OpenFile: err = %v, want os.ErrNotExist", err)
	}
	if f != nil {
		t.Errorf("OpenFile returned a non-nil File alongside an error: %#v", f)
	}

	if err := fsys.Remove(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Remove: err = %v, want os.ErrNotExist", err)
	}
	if _, err := fsys.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Stat: err = %v, want os.ErrNotExist", err)
	}
	if _, err := fsys.ReadDir(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ReadDir: err = %v, want os.ErrNotExist", err)
	}
	if err := fsys.Rename(missing, filepath.Join(dir, "other")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Rename: err = %v, want os.ErrNotExist", err)
	}
}

// And the happy path, so the passthrough is proved to pass things through.
func TestOSPerformsRealOperations(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.OS()

	sub := filepath.Join(dir, "a", "b")
	if err := fsys.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	name := filepath.Join(sub, "f")
	f, err := fsys.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if n, err := f.Write([]byte("hello")); err != nil || n != 5 {
		t.Fatalf("Write: %d, %v", n, err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := f.Truncate(4); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(name)
	if err != nil || string(got) != "hell" {
		t.Errorf("file holds %q (%v), want %q", got, err, "hell")
	}
	if entries, err := fsys.ReadDir(sub); err != nil || len(entries) != 1 {
		t.Errorf("ReadDir: %d entries, %v", len(entries), err)
	}
	if err := fsys.Rename(name, name+".2"); err != nil {
		t.Errorf("Rename: %v", err)
	}
	if err := fsys.Remove(name + ".2"); err != nil {
		t.Errorf("Remove: %v", err)
	}
}
