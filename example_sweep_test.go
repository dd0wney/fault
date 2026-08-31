package fault_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// TestTheSweepLoopFromTheDocumentation is the package documentation's example,
// made real and kept honest by the compiler.
//
// WHY THIS IS A TEST AND NOT AN Example FUNCTION. [fault.Sweep] takes a
// *testing.T, so the loop cannot appear in an Example, which Go gives no test
// context. The documented snippet would therefore be the one piece of this
// package that nothing compiles -- and this repository has already shipped a
// doc example that could not pass. This test is that snippet, self-contained,
// so a reader can copy it and a change to the API breaks the build here rather
// than on a stranger's first afternoon.
//
// The scenario is deliberately small: create a directory, write a file, sync
// it, close it. Four operations, and every one of them can fail.
func TestTheSweepLoopFromTheDocumentation(t *testing.T) {
	points := 0

	for n, p := range fault.Sweep(t) {
		points++

		dir := t.TempDir()
		fsys := faultfs.New(p, faultfs.OS())

		err := writeOneFile(fsys, filepath.Join(dir, "data"))

		// THE LOOP BODY IS THE CHECK. Assert the invariant here.
		//
		// This one is the invariant most callers actually want: whatever
		// failed, the store must not be left holding a descriptor.
		if leaked := fsys.Outstanding(); leaked != 0 {
			t.Errorf("op %d: %d handle(s) still open after %v: %v",
				n, leaked, err, fsys.OpenPaths())
		}
	}

	// CONTROL -- the sweep visited real fault points.
	//
	// Sweep ends when a pass completes without the fault firing. If the
	// scenario performed no operation the adapter could fail, that happens on
	// the first pass and the assertion above never ran against a failure.
	if points < 2 {
		t.Fatalf("the sweep visited %d point(s), so nothing was ever injected "+
			"and the invariant was never tested against an error", points)
	}
	t.Logf("visited %d fault points", points)
}

// writeOneFile is the code under test: ordinary code, written without any
// knowledge of this package.
func writeOneFile(fsys faultfs.FS, path string) error {
	if err := fsys.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	f, err := fsys.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	// The deferred close is what keeps Outstanding at zero on every error path
	// below. Remove it and this test fails at the points that inject after the
	// open, which is exactly the defect the invariant exists to catch.
	defer func() { _ = f.Close() }()

	if _, err := f.Write([]byte("payload")); err != nil {
		return err
	}
	return f.Sync()
}
