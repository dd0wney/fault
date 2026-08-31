package fs_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// OpenPaths names what Outstanding counts.
//
// A count fails a sweep and cannot fix one. A leak found by Outstanding sends
// the reader back to the code under test with one number and no call site,
// which is the position this method exists to remove: driving graphdb's store
// through this package reported four leaked handles and could not say which
// file, so the harness grew a name-tracking wrapper by hand to find out. The
// answer was one file on four error paths of one constructor.
func TestOpenPathsNamesTheHandleThatOutstandingCounts(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())

	if got := fsys.OpenPaths(); len(got) != 0 {
		t.Errorf("before any open, OpenPaths = %v, want empty", got)
	}

	f, err := fsys.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if got := fsys.OpenPaths(); !slices.Equal(got, []string{name}) {
		t.Errorf("after one open, OpenPaths = %v, want [%s]", got, name)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fsys.OpenPaths(); len(got) != 0 {
		t.Errorf("after the close, OpenPaths = %v, want empty", got)
	}
}

// One entry per outstanding HANDLE, not per distinct name. A file opened twice
// and closed once is still open, and a list that folded the two together would
// report it as closed.
func TestOpenPathsCountsEachHandleSeparately(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())

	first, err := fsys.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fsys.OpenFile(name, os.O_RDONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if got := fsys.OpenPaths(); !slices.Equal(got, []string{name, name}) {
		t.Errorf("two handles on one name give OpenPaths = %v, want it twice", got)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if got := fsys.OpenPaths(); !slices.Equal(got, []string{name}) {
		t.Errorf("after closing one of two, OpenPaths = %v, want [%s]", got, name)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

// The list is sorted, so a caller can compare it and a failure message reads
// the same on every run.
func TestOpenPathsIsSorted(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())

	for _, base := range []string{"c", "a", "b"} {
		f, err := fsys.OpenFile(filepath.Join(dir, base), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
	}

	want := []string{filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "c")}
	if got := fsys.OpenPaths(); !slices.Equal(got, want) {
		t.Errorf("OpenPaths = %v, want %v", got, want)
	}
}

// THE INVARIANT THAT MAKES THE PAIR TRUSTWORTHY, swept across every fault
// point rather than asserted once.
//
// Outstanding and OpenPaths must never disagree. If they could, a sweep would
// fail on one and be debugged with the other, and the two numbers would send
// the reader to different places.
func TestOpenPathsAndOutstandingNeverDisagree(t *testing.T) {
	dir := t.TempDir()
	checked := 0

	for n, p := range fault.Sweep(t) {
		fsys := faultfs.New(p, faultfs.OS())

		f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
		if err == nil {
			_, _ = f.Write([]byte("x"))
			_ = f.Sync()
			_ = f.Close()
		}

		checked++
		if got, want := len(fsys.OpenPaths()), fsys.Outstanding(); got != want {
			t.Errorf("point %d: OpenPaths reports %d handles, Outstanding reports %d", n, got, want)
		}
	}

	// CONTROL -- the sweep visited fault points, so the agreement above was
	// tested on error paths and not only on the one clean pass.
	if checked < 2 {
		t.Fatalf("the sweep visited %d points, so the invariant was never tested "+
			"against an injected failure", checked)
	}
}

// MaxOutstanding is the high-water mark, and it exists to make a leak check
// prove it was live.
//
// Outstanding() == 0 after a scenario is the PASS condition for "nothing
// leaked". It is also what a scenario that never opened a file returns. The
// two are indistinguishable, so a sweep over a component that held no handle
// reports a clean leak check having compared 0 against 0.
//
// Three separate sweeps against a peer project hand-wrote the same six lines
// to guard that, tracking a high-water mark themselves. This is those lines.
func TestMaxOutstandingRemembersTheHighWaterMark(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())

	if got := fsys.MaxOutstanding(); got != 0 {
		t.Errorf("before any open, MaxOutstanding = %d, want 0", got)
	}

	first, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fsys.OpenFile(filepath.Join(dir, "b"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if got := fsys.MaxOutstanding(); got != 2 {
		t.Errorf("with two handles open, MaxOutstanding = %d, want 2", got)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}

	// THE PROPERTY THAT MAKES IT USEFUL. Outstanding falls back to zero and
	// the high-water mark does not, so a caller can tell "released everything"
	// from "never held anything".
	if got := fsys.Outstanding(); got != 0 {
		t.Errorf("after both closes, Outstanding = %d, want 0", got)
	}
	if got := fsys.MaxOutstanding(); got != 2 {
		t.Errorf("after both closes, MaxOutstanding = %d, want 2 -- it must not "+
			"fall back, or it cannot tell a released handle from an absent one", got)
	}
}

// A filesystem that never opened anything reports zero, which is the reading
// that lets a caller fail its own vacuous check.
func TestMaxOutstandingIsZeroWhenNothingWasEverOpened(t *testing.T) {
	dir := t.TempDir()

	for n, p := range fault.Sweep(t) {
		fsys := faultfs.New(p, faultfs.OS())

		// Only operations that open nothing.
		_ = fsys.MkdirAll(filepath.Join(dir, "d"), 0o750)
		_, _ = fsys.Stat(dir)

		if got := fsys.MaxOutstanding(); got != 0 {
			t.Errorf("point %d: no file was opened, MaxOutstanding = %d, want 0", n, got)
		}
	}
}

// MaxOutstanding can never be below Outstanding, swept across every fault
// point rather than asserted once.
func TestMaxOutstandingNeverFallsBelowOutstanding(t *testing.T) {
	dir := t.TempDir()
	checked := 0

	for n, p := range fault.Sweep(t) {
		fsys := faultfs.New(p, faultfs.OS())

		f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
		if err == nil {
			_, _ = f.Write([]byte("x"))
			if hi, now := fsys.MaxOutstanding(), fsys.Outstanding(); hi < now {
				t.Errorf("point %d: MaxOutstanding %d is below Outstanding %d while open",
					n, hi, now)
			}
			_ = f.Close()
		}

		checked++
		if hi, now := fsys.MaxOutstanding(), fsys.Outstanding(); hi < now {
			t.Errorf("point %d: MaxOutstanding %d is below Outstanding %d after close",
				n, hi, now)
		}
	}

	// CONTROL -- the sweep injected failures, so the comparison above ran on
	// error paths and not only on the one clean pass.
	if checked < 2 {
		t.Fatalf("the sweep visited %d points, so the invariant was never tested "+
			"against an injected failure", checked)
	}
}
