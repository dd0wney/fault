package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault"
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

// The Op strings were measured against the real os package rather than
// remembered, and three of the first guesses were wrong: MkdirAll reports
// "mkdir" and not "mkdirall", ReadDir reports "open" because it opens the
// directory first, and Rename reports an *os.LinkError.
//
// That decision had no test. Mutation testing cannot find it either -- no
// mutation operator rewrites a string literal -- so a later "tidy-up" that
// makes the Op strings match the method names would pass the entire suite while
// making every injected error a lie about what the filesystem does.
//
// A deliberate decision that no test asserts is indistinguishable from an
// accident. This is the assertion.
func TestInjectedOpStringsMatchTheRealOnes(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "no-such-directory", "f")
	real := faultfs.OS()

	// Each case makes the REAL os package fail, and reads the Op it reported.
	cases := []struct {
		name string
		real func() error
		inj  func(faultfs.FS) error
	}{
		{"OpenFile",
			func() error { _, err := real.OpenFile(missing, os.O_RDONLY, 0); return err },
			func(f faultfs.FS) error { _, err := f.OpenFile("x", os.O_RDONLY, 0); return err }},
		{"Remove",
			func() error { return real.Remove(missing) },
			func(f faultfs.FS) error { return f.Remove("x") }},
		{"Stat",
			func() error { _, err := real.Stat(missing); return err },
			func(f faultfs.FS) error { _, err := f.Stat("x"); return err }},
		{"ReadDir",
			func() error { _, err := real.ReadDir(missing); return err },
			func(f faultfs.FS) error { _, err := f.ReadDir("d"); return err }},
		{"MkdirAll",
			// A directory under a regular file: the real MkdirAll reports the
			// failing syscall, not the helper that called it.
			func() error { return real.MkdirAll(filepath.Join(regular, "a", "b"), 0o700) },
			func(f faultfs.FS) error { return f.MkdirAll("d", 0o700) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var realPath *os.PathError
			if !errors.As(tc.real(), &realPath) {
				t.Fatalf("the control produced no *os.PathError, so this compares nothing")
			}

			var injected error
			for n, p := range fault.Sweep(t) {
				err := tc.inj(faultfs.New(p, newStub()))
				if n == 1 {
					injected = err
				}
			}
			var injPath *os.PathError
			if !errors.As(injected, &injPath) {
				t.Fatalf("injected error is %T, want *os.PathError: %v", injected, injected)
			}

			if injPath.Op != realPath.Op {
				t.Errorf("injected Op = %q, but the real os package reports %q",
					injPath.Op, realPath.Op)
			}
		})
	}
}
