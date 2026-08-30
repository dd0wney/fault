package fs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// A handle opened and closed leaves nothing outstanding.
func TestOutstandingIsZeroAfterAMatchedOpenAndClose(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())

	f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if got := fsys.Outstanding(); got != 1 {
		t.Errorf("after one open, Outstanding = %d, want 1", got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fsys.Outstanding(); got != 0 {
		t.Errorf("after the close, Outstanding = %d, want 0", got)
	}
}

// An open that FAILED handed nothing out, so it must not count. Otherwise a
// sweep reports a leak on every pass that fails an open, which is every pass
// that tests the thing this package exists to test.
func TestAFailedOpenLeavesNothingOutstanding(t *testing.T) {
	dir := t.TempDir()

	for n, p := range fault.Sweep(t) {
		fsys := faultfs.New(p, faultfs.OS())
		f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			if got := fsys.Outstanding(); got != 0 {
				t.Errorf("op %d: a failed open left %d outstanding, want 0", n, got)
			}
			continue
		}
		_ = f.Close()
	}
}

// A close that FAILS still releases the descriptor, because POSIX guarantees
// it and this package's Close closes the real handle either way. So a failed
// close must still decrement, or a sweep reports a leak the code under test
// cannot avoid.
func TestAFailedCloseStillReturnsTheHandle(t *testing.T) {
	dir := t.TempDir()
	var sawFailedClose bool

	for n, p := range fault.Sweep(t) {
		fsys := faultfs.New(p, faultfs.OS())
		f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		cerr := f.Close()
		if cerr != nil {
			sawFailedClose = true
		}
		if got := fsys.Outstanding(); got != 0 {
			t.Errorf("op %d: after a close that returned %v, Outstanding = %d, want 0",
				n, cerr, got)
		}
	}

	if !sawFailedClose {
		t.Error("no pass injected a failed close, so the rule is untested")
	}
}

// The property this exists for: a store that leaks a handle on its error path
// is caught, and one that does not is not.
//
// leaky opens a second file and returns early without closing it when the
// second open succeeds and a later operation fails. tidy always closes.
func TestOutstandingCatchesALeakOnTheErrorPath(t *testing.T) {
	// held collects handles the store leaked, so the TEST can close them after
	// measuring. Windows cannot delete a file that still has an open handle,
	// and t.TempDir cleanup fails there — which is how four leaked handles in
	// this repository's own tests were found earlier today. Measuring first and
	// closing after leaves the measurement untouched.
	run := func(t *testing.T, store func(faultfs.FS, string, *[]faultfs.File) error) int {
		t.Helper()
		dir := t.TempDir()
		worst := 0
		for _, p := range fault.Sweep(t) {
			fsys := faultfs.New(p, faultfs.OS())

			var held []faultfs.File
			_ = store(fsys, dir, &held)

			if got := fsys.Outstanding(); got > worst {
				worst = got
			}
			for _, f := range held {
				_ = f.Close()
			}
		}
		return worst
	}

	leaky := func(fsys faultfs.FS, dir string, held *[]faultfs.File) error {
		f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		// No defer. The store returns without closing, which is the defect.
		if _, err := f.Write([]byte("x")); err != nil {
			*held = append(*held, f)
			return err
		}
		return f.Close()
	}

	tidy := func(fsys faultfs.FS, dir string, _ *[]faultfs.File) error {
		f, err := fsys.OpenFile(filepath.Join(dir, "b"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		_, err = f.Write([]byte("x"))
		return err
	}

	if got := run(t, leaky); got == 0 {
		t.Error("the leaky store leaked nothing, so Outstanding cannot detect a leak at all")
	}
	if got := run(t, tidy); got != 0 {
		t.Errorf("the tidy store was reported as leaking %d handles, which is a false positive", got)
	}
}
