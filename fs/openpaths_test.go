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
