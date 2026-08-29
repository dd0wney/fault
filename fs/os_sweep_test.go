package fs_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// atomicWrite is a miniature store. It writes to a temporary file, syncs it,
// closes it, and renames it into place -- the standard way to make a file
// update atomic, and the pattern every real store uses.
//
// Its cleanup paths perform operations of their own, which is deliberate: a
// cleanup that allocates or writes in order to clean up is exactly the code an
// exhaustive sweep exists to reach, and a single hand-written fault test never
// does.
func atomicWrite(fsys faultfs.FS, dir string, data []byte) error {
	if err := fsys.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	tmp := filepath.Join(dir, "data.tmp")
	final := filepath.Join(dir, "data")

	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		fsys.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		fsys.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		fsys.Remove(tmp)
		return err
	}
	return fsys.Rename(tmp, final)
}

// TestSweepOverARealFilesystem drives the whole library against a real disk.
//
// Every other test in this module runs against a stub, which leaves the central
// claim untested: that a real store performing real I/O survives a fault at
// every point in its sequence. This is that test.
//
// The invariant is the one an atomic write promises. After a fault at any
// point, the destination file is either absent or complete. It is never
// partially written, and it never holds truncated or torn content.
func TestSweepOverARealFilesystem(t *testing.T) {
	root := t.TempDir()
	want := []byte("the quick brown fox jumps over the lazy dog")

	fdsBefore := openDescriptors(t)

	var passes, failed int
	for n, p := range fault.Sweep(t) {
		passes++

		// A fresh directory each pass, so a pass observes only its own
		// outcome. Sharing one would let an earlier success satisfy a later
		// pass's invariant, and the sweep would prove less than it appears to.
		dir := filepath.Join(root, strconv.Itoa(n))

		writeErr := atomicWrite(faultfs.New(p, faultfs.OS()), dir, want)
		if writeErr != nil {
			failed++
		}

		got, readErr := os.ReadFile(filepath.Join(dir, "data"))
		switch {
		case errors.Is(readErr, os.ErrNotExist):
			if writeErr == nil {
				t.Errorf("op %d: atomicWrite reported success, but no file exists", n)
			}
		case readErr != nil:
			t.Errorf("op %d: reading the destination: %v", n, readErr)
		case !bytes.Equal(got, want):
			t.Errorf("op %d: destination holds %q, want %q (write returned %v)",
				n, got, want, writeErr)
		}
	}

	// Positive control. A sweep that injected nothing would satisfy every
	// assertion above and prove nothing at all.
	if failed == 0 {
		t.Error("no pass failed: the sweep injected nothing, so it proved nothing")
	}
	if passes < 6 {
		t.Errorf("%d passes: atomicWrite performs at least 6 operations, so the "+
			"sweep should visit at least that many", passes)
	}
	t.Logf("%d passes, %d of them faulted", passes, failed)

	// The Close exception, observed end to end. Every pass opens a file, and
	// one pass in six trips on the close. If a failed Close skipped its
	// release, this is where it would show.
	if fdsAfter := openDescriptors(t); fdsBefore >= 0 && fdsAfter > fdsBefore {
		t.Errorf("descriptors: %d before, %d after: a failed Close leaked a handle",
			fdsBefore, fdsAfter)
	}
}

// openDescriptors counts this process's open file descriptors, or returns -1
// where the platform does not expose them. A count that cannot be taken must
// not read as a count of zero.
func openDescriptors(t *testing.T) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		return -1
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}
