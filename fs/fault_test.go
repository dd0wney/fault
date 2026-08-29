package fs_test

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// stubFS records what reached the real filesystem. Nothing here touches a disk:
// these tests are about the adapter, not about the os package.
type stubFS struct{ calls map[string]int }

func newStub() *stubFS { return &stubFS{calls: map[string]int{}} }

func (s *stubFS) note(op string) { s.calls[op]++ }

func (s *stubFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	s.note("OpenFile")
	return stubFile{s}, nil
}
func (s *stubFS) Remove(name string) error                     { s.note("Remove"); return nil }
func (s *stubFS) Rename(oldname, newname string) error         { s.note("Rename"); return nil }
func (s *stubFS) Stat(name string) (os.FileInfo, error)        { s.note("Stat"); return nil, nil }
func (s *stubFS) MkdirAll(name string, perm os.FileMode) error { s.note("MkdirAll"); return nil }
func (s *stubFS) ReadDir(name string) ([]os.DirEntry, error)   { s.note("ReadDir"); return nil, nil }

type stubFile struct{ s *stubFS }

func (f stubFile) Read(b []byte) (int, error)  { f.s.note("Read"); return 0, nil }
func (f stubFile) Write(b []byte) (int, error) { f.s.note("Write"); return len(b), nil }
func (f stubFile) Sync() error                 { f.s.note("Sync"); return nil }
func (f stubFile) Truncate(int64) error        { f.s.note("Truncate"); return nil }
func (f stubFile) Close() error                { f.s.note("Close"); return nil }

// one names each operation and a scenario that performs exactly that one.
var one = map[string]func(faultfs.FS) error{
	"OpenFile": func(f faultfs.FS) error { _, err := f.OpenFile("x", os.O_RDWR, 0o600); return err },
	"Remove":   func(f faultfs.FS) error { return f.Remove("x") },
	"Rename":   func(f faultfs.FS) error { return f.Rename("x", "y") },
	"Stat":     func(f faultfs.FS) error { _, err := f.Stat("x"); return err },
	"MkdirAll": func(f faultfs.FS) error { return f.MkdirAll("d", 0o700) },
	"ReadDir":  func(f faultfs.FS) error { _, err := f.ReadDir("d"); return err },
}

// Contract rule 3: every operation must call Trip. An operation that skips the
// call is invisible to the sweep, and the sweep then reports a clean walk over
// a sequence it never visited.
//
// The sweep itself is the counter. A scenario performing exactly one operation
// runs exactly two passes: one that fails the operation, and one that proves
// the sequence ended. An operation that never calls Trip produces one pass and
// ErrNoOperations.
func TestEveryOperationCallsTrip(t *testing.T) {
	for name, op := range one {
		t.Run(name, func(t *testing.T) {
			passes := 0
			for _, p := range fault.Sweep(t) {
				passes++
				_ = op(faultfs.New(p, newStub()))
			}
			// An operation that never calls Trip produces one pass, and Sweep
			// fails the test with "the scenario performed no operations".
			if passes != 2 {
				t.Errorf("%s: %d passes, want 2", name, passes)
			}
		})
	}
}

// Contract rule 2: a failed operation must not reach the real filesystem.
func TestAFailedOperationIsNotPerformed(t *testing.T) {
	for name, op := range one {
		t.Run(name, func(t *testing.T) {
			base := newStub()
			for n, p := range fault.Sweep(t) {
				err := op(faultfs.New(p, base))
				if n != 1 {
					continue // only the first pass fails the first operation
				}
				if err == nil {
					t.Errorf("%s returned nil, want an error", name)
				}
				if got := base.calls[name]; got != 0 {
					t.Errorf("%s reached the real filesystem %d times, want 0", name, got)
				}
			}
		})
	}
}

// Contract rule 4: the error must be one a real filesystem would produce, so a
// store that distinguishes EIO from ENOSPC can be tested at all.
func TestTheInjectedErrorIsRealistic(t *testing.T) {
	var err error
	for n, p := range fault.Sweep(t) {
		_, openErr := faultfs.New(p, newStub()).OpenFile("x", os.O_RDWR, 0o600)
		if n == 1 {
			err = openErr
		}
	}

	var pathErr *os.PathError
	if !errors.As(err, &pathErr) {
		t.Fatalf("err = %v (%T), want an *os.PathError", err, err)
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		t.Errorf("err = %v, want it to wrap a syscall.Errno", err)
	}
}

// The interesting failure points live on an open file: between a write and its
// sync, and on the close after a failed sync. An adapter that fails only the FS
// methods misses every one of them.
func TestAnOpenFileFailsToo(t *testing.T) {
	passes := 0
	for _, p := range fault.Sweep(t) {
		passes++
		f, openErr := faultfs.New(p, newStub()).OpenFile("x", os.O_RDWR, 0o600)
		if openErr != nil {
			continue // this pass failed the open; the file operations do not run
		}
		_, _ = f.Write([]byte("hello"))
		_ = f.Sync()
		_ = f.Close()
	}

	// open, write, sync, close, plus the pass that proves the sequence ended.
	if passes != 5 {
		t.Errorf("%d passes, want 5: the File methods must call Trip as well", passes)
	}
}

// Contract rule 4 has no automatic check: no assertion can know what the real
// filesystem would have returned. What CAN be pinned is the error shape, and
// the shapes were measured against the os package rather than remembered.
//
//	Remove, Stat, OpenFile, MkdirAll, ReadDir -> *os.PathError
//	Rename                                    -> *os.LinkError (it has two paths)
//
// A store that type-switches on the error is exactly the store worth sweeping.
// If the adapter returns the wrong type, that store takes a branch under the
// sweep that it can never take in production.
func TestTheErrorShapeMatchesTheOsPackage(t *testing.T) {
	for name, op := range one {
		t.Run(name, func(t *testing.T) {
			var got error
			for n, p := range fault.Sweep(t) {
				err := op(faultfs.New(p, newStub()))
				if n == 1 {
					got = err
				}
			}

			var errno syscall.Errno
			if !errors.As(got, &errno) {
				t.Errorf("err = %v, want it to wrap a syscall.Errno", got)
			}

			if name == "Rename" {
				var linkErr *os.LinkError
				if !errors.As(got, &linkErr) {
					t.Errorf("err = %v (%T), want *os.LinkError: os.Rename reports two paths", got, got)
				}
				return
			}
			var pathErr *os.PathError
			if !errors.As(got, &pathErr) {
				t.Errorf("err = %v (%T), want *os.PathError", got, got)
			}
		})
	}
}

// Close is the one method that must not skip its operation.
//
// Contract rule 2 says a failed operation must not be performed, so that it has
// no effect the caller can observe. Releasing a descriptor is not such an
// effect -- it is the operating system reclaiming its own -- and a real failing
// close(2) releases it regardless, which POSIX guarantees.
//
// The cost of getting this wrong appears far from the cause. The sweep opens a
// file on every pass and runs up to 512 of them, so a Close that returns early
// leaks one descriptor per pass. Against a real filesystem the run dies partway
// through with "too many open files", and the blame lands on the code under
// test.
func TestAFailedCloseStillReleasesTheHandle(t *testing.T) {
	base := newStub()

	var closeErrs int
	for _, p := range fault.Sweep(t) {
		f, openErr := faultfs.New(p, base).OpenFile("x", os.O_RDWR, 0o600)
		if openErr != nil {
			continue // this pass failed the open, so there is nothing to close
		}
		if err := f.Close(); err != nil {
			closeErrs++
		}
	}

	// open + close is a two-operation scenario, so three passes: one fails the
	// open, one fails the close, and one proves the sequence ended. Two of
	// those three reach Close, and both must reach the real handle.
	if closeErrs != 1 {
		t.Errorf("%d passes reported a close error, want 1", closeErrs)
	}
	if got := base.calls["Close"]; got != 2 {
		t.Errorf("the real Close ran %d times, want 2: a failed Close still releases the handle", got)
	}
}
