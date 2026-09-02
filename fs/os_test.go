package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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

// errShape names the ONE concrete type a method really returns, and reads its
// Op field.
//
// A single helper that tried *os.PathError and then *os.LinkError would be
// shorter and would pass for a method that returned the wrong one of the two.
// That difference is what this table exists to pin: Rename reports an
// *os.LinkError and every other method here reports an *os.PathError. A caller
// that type-switches on the two cannot be tested against an adapter that
// confuses them, and no other gate in this repository can see the confusion.
type errShape struct {
	name string
	op   func(error) (string, bool)
}

var (
	pathError = errShape{"*os.PathError", func(err error) (string, bool) {
		var e *os.PathError
		if !errors.As(err, &e) {
			return "", false
		}
		return e.Op, true
	}}

	linkError = errShape{"*os.LinkError", func(err error) (string, bool) {
		var e *os.LinkError
		if !errors.As(err, &e) {
			return "", false
		}
		return e.Op, true
	}}
)

// opFixture carries the paths a control needs to make the REAL os package
// fail.
type opFixture struct {
	real    faultfs.FS
	dir     string
	regular string
	missing string
}

// opCase is one row: a way to make the real os package fail, the same
// operation under injection, and the error shape both must have.
//
// realClass names the errno the real route produces. It is never the class
// this package injects: no unprivileged route makes a path operation report
// EIO or ENOSPC (realerr_test.go records what the two devices give each FS
// method). differs lists, by name, the predicates in predicate_test.go whose
// answer the class difference changes: errors.Is(syscall.EIO) on every row,
// because the injected errno is EIO and the real one is not, and
// os.IsNotExist on the rows whose real errno is ENOENT. The predicate table
// requires those to DISAGREE and every other predicate to AGREE, so a stale
// entry here fails as loudly as a missing one. An earlier version excluded
// every class predicate as "differs by construction", and a reviewer showed
// that three of the five agree on every row; the exclusion was a claim, and
// this list is a checked one.
//
// differsOnWindows replaces differs there, for the one row whose real class is
// not the same on both. MEASURED 2026-09-02 by Windows CI on the first run of
// this table: MkdirAll under a regular file reports ERROR_PATH_NOT_FOUND on
// Windows, and os.IsNotExist answers true to it, where POSIX reports ENOTDIR
// and os.IsNotExist answers false. The same tightness that makes a stale
// entry fail is what found this: the row said "agree", Windows said "differ",
// and the test refused rather than skipped.
type opCase struct {
	name             string
	real             func(opFixture) error
	inj              func(faultfs.FS) error
	shape            errShape
	realClass        string
	differs          []string
	differsOnWindows []string
}

// The predicates whose answer a real ENOENT and an injected EIO differ on.
var enoentDiffers = []string{"errors.Is(syscall.EIO)", "os.IsNotExist"}

// opCases is package-level so that TestTheOpTableCoversTheFSInterface can
// compare it against fs.FS. A table local to a test function is invisible to
// reflection, and that is how Rename went missing from it.
//
// It is THE FS-level table. The Op comparison below and the FS-level predicate
// comparison in predicate_test.go both read it, so there is no second list to
// drift from this one.
var opCases = []opCase{
	{"OpenFile",
		func(f opFixture) error { _, err := f.real.OpenFile(f.missing, os.O_RDONLY, 0); return err },
		func(f faultfs.FS) error { _, err := f.OpenFile("x", os.O_RDONLY, 0); return err },
		pathError, "ENOENT, from a missing path", enoentDiffers, nil},
	{"Remove",
		func(f opFixture) error { return f.real.Remove(f.missing) },
		func(f faultfs.FS) error { return f.Remove("x") },
		pathError, "ENOENT, from a missing path", enoentDiffers, nil},
	{"Stat",
		func(f opFixture) error { _, err := f.real.Stat(f.missing); return err },
		func(f faultfs.FS) error { _, err := f.Stat("x"); return err },
		pathError, "ENOENT, from a missing path", enoentDiffers, nil},
	{"ReadDir",
		func(f opFixture) error { _, err := f.real.ReadDir(f.missing); return err },
		func(f faultfs.FS) error { _, err := f.ReadDir("d"); return err },
		pathError, "ENOENT, from a missing path", enoentDiffers, nil},
	{"MkdirAll",
		// A directory under a regular file: the real MkdirAll reports the
		// failing syscall, not the helper that called it.
		func(f opFixture) error { return f.real.MkdirAll(filepath.Join(f.regular, "a", "b"), 0o700) },
		func(f faultfs.FS) error { return f.MkdirAll("d", 0o700) },
		pathError, "ENOTDIR on POSIX and ERROR_PATH_NOT_FOUND on Windows, from a directory under a regular file",
		[]string{"errors.Is(syscall.EIO)"},
		enoentDiffers},
	{"Rename",
		// The one row whose error type differs, and the row that was missing
		// when this table was first compared against the interface. Renaming a
		// path that does not exist is the portable way to make the real one
		// fail.
		func(f opFixture) error { return f.real.Rename(f.missing, filepath.Join(f.dir, "renamed")) },
		func(f faultfs.FS) error { return f.Rename("a", "b") },
		linkError, "ENOENT, from a missing path", enoentDiffers, nil},
}

// newOpFixture builds the paths the real routes need.
func newOpFixture(t *testing.T) opFixture {
	t.Helper()
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return opFixture{
		real:    faultfs.OS(),
		dir:     dir,
		regular: regular,
		missing: filepath.Join(dir, "no-such-directory", "f"),
	}
}

// injectedFSError returns what the wrapper injects for inj on the pass that
// arms its first operation, driven through a sweep rather than a hand-armed
// Points, for the reason doc.go gives under "Testing an adapter".
func injectedFSError(t *testing.T, inj func(faultfs.FS) error) error {
	t.Helper()
	var injected error
	for n, p := range fault.Sweep(t) {
		err := inj(faultfs.New(p, newStub()))
		if n == 1 {
			injected = err
		}
	}
	return injected
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
	fixture := newOpFixture(t)

	for _, tc := range opCases {
		t.Run(tc.name, func(t *testing.T) {
			// The Op string is not portable, and CI found it rather than
			// reasoning finding it. On Windows the real os package reports the
			// Win32 API it called -- Stat gives "GetFileAttributesEx" on one Go
			// release and "CreateFile" on another. The two Go versions in the
			// matrix disagree with each other, so there is no value an adapter
			// could hardcode that would be right.
			//
			// The shape assertions in fault_test.go are portable and stay in
			// force everywhere. This one pins the mapping where it is stable.
			if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				t.Skipf("the Op string is platform-specific on %s", runtime.GOOS)
			}

			realOp, ok := tc.shape.op(tc.real(fixture))
			if !ok {
				t.Fatalf("the control produced no %s, so this compares nothing", tc.shape.name)
			}

			injected := injectedFSError(t, tc.inj)
			injOp, ok := tc.shape.op(injected)
			if !ok {
				t.Fatalf("injected error is %T, want %s: %v", injected, tc.shape.name, injected)
			}

			if injOp != realOp {
				t.Errorf("injected Op = %q, but the real os package reports %q", injOp, realOp)
			}
		})
	}
}

// Contract rule 4, one level up from TestInjectedOpStringsMatchTheRealOnes.
//
// That test drives a table and proves every method IN THE TABLE reports the Op
// the real os package reports. It says nothing about a method the table omits,
// and the table is written by hand. This one compares the table against the
// interface itself.
//
// This is fs/contract_test.go's check, applied to a SECOND hand-written table
// in the same package. That check compares the operation table against fs.FS
// and could not see this one, because this one was local to a test function.
func TestTheOpTableCoversTheFSInterface(t *testing.T) {
	iface := reflect.TypeOf((*faultfs.FS)(nil)).Elem()

	inTable := make(map[string]bool, len(opCases))
	for _, c := range opCases {
		inTable[c.name] = true
	}

	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		if !inTable[name] {
			t.Errorf("fs.FS has %s and the Op table does not, so nothing proves the error it "+
				"injects has the shape and the Op string the os package really reports. "+
				"An injected error that a caller's predicate answers differently sends that "+
				"caller down a branch the real failure would not.", name)
		}
	}

	for name := range inTable {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("the Op table has %s and fs.FS does not", name)
		}
	}

	// The positive control. If the interface reported no methods, both loops
	// above would pass having compared nothing.
	if iface.NumMethod() == 0 {
		t.Fatal("reflection reported no methods on fs.FS, so this check compared nothing")
	}
}
