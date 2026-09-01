package fs_test

import (
	"errors"
	"os"
	"runtime"
	"syscall"
	"testing"
)

// Real errors of the two classes this package injects, for contract rule 4.
//
// Rule 4's sharp form is at doc.go:69: an injected error must be
// indistinguishable from the real one to every predicate the code under test
// applies to it. Testing that needs a REAL error of the class this package
// injects, and this package injects exactly two — syscall.EIO from New, and
// syscall.ENOSPC from NewShortWrite.
//
// docs/plans/2026-09-01-robustness-evidence.md predicted neither was portably
// reachable and planned to freeze a hand measurement as test data. MEASURED
// 2026-09-01, that prediction is wrong on Linux. Both are reachable from an
// ordinary unprivileged test, through two character devices:
//
//	ENOSPC   write to /dev/full, which reports a full disk on every write
//	EIO      read or write /proc/self/mem at offset 0, which is never mapped
//
// A live control beats a frozen figure, and the difference is the whole reason
// to prefer it: a frozen figure cannot notice when the thing it describes
// changes. This repository has already recorded that shape — "a recorded number
// that ages is not a broken gate".
//
// Measured on linux/amd64, kernel 7.1.10, go1.27.0. Five consecutive runs gave
// bit-identical results:
//
//	Write    /dev/full        *os.PathError{Op: "write", Err: ENOSPC(28)}
//	WriteAt  /dev/full        *os.PathError{Op: "write", Err: ENOSPC(28)}
//	Read     /proc/self/mem   *os.PathError{Op: "read",  Err: EIO(5)}
//	Write    /proc/self/mem   *os.PathError{Op: "write", Err: EIO(5)}
//
// WHAT THESE ROUTES DO NOT REACH, stated rather than left to be discovered.
// Sync and Truncate report EINVAL on both devices. Close reports nothing at
// all. And no FS-level method reaches either errno by these routes: Remove and
// Rename give EACCES, MkdirAll and ReadDir give ENOTDIR, and Stat succeeds. A
// predicate table row for one of those compares shape and Op only, and has to
// say which it is — a row that quietly compares less than its neighbours is the
// defect this whole file exists to make visible.
//
// ON LINUX A BROKEN ROUTE FAILS, IT DOES NOT SKIP. If /dev/full stops reporting
// ENOSPC, the control is gone and every predicate row built on it stops
// comparing, silently, while CI stays green. That is the false green this
// module is about. Elsewhere the devices do not exist at all, so a skip is the
// honest answer, and CI already prints the skipped count for every matrix leg.

// fullDevice is the ENOSPC route: a character device that accepts a handle and
// then reports a full disk on every write.
const fullDevice = "/dev/full"

// unmappedMemory is the EIO route. Reading or writing /proc/<pid>/mem at an
// address the process has not mapped reports EIO, and address 0 is never
// mapped. yama's ptrace_scope restricts reading ANOTHER process's memory, so it
// does not reach this: measured with ptrace_scope=1, which is the default on
// this machine and the stricter of the two common settings.
const unmappedMemory = "/proc/self/mem"

// requireRealErrors skips where the routes do not exist, and only there.
func requireRealErrors(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("the real-error controls use %s and %s, which are Linux-only; "+
			"the predicate comparison for this method does not run on %s",
			fullDevice, unmappedMemory, runtime.GOOS)
	}
}

// onFullDevice runs do against a handle on /dev/full and returns what it
// reported. Use it to obtain a real ENOSPC.
func onFullDevice(t *testing.T, do func(*os.File) error) error {
	t.Helper()
	requireRealErrors(t)

	f, err := os.OpenFile(fullDevice, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("the ENOSPC control cannot open %s, so it compares nothing: %v", fullDevice, err)
	}
	defer func() { _ = f.Close() }()

	return do(f)
}

// onUnmappedMemory runs do against a handle on /proc/self/mem. Every offset the
// caller touches must be 0, which no process maps. Use it to obtain a real EIO.
func onUnmappedMemory(t *testing.T, do func(*os.File) error) error {
	t.Helper()
	requireRealErrors(t)

	f, err := os.OpenFile(unmappedMemory, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("the EIO control cannot open %s, so it compares nothing: %v", unmappedMemory, err)
	}
	defer func() { _ = f.Close() }()

	return do(f)
}

// A control needs its own control, and this repository has recorded three
// instances in one day of one that could not report the negative: a sed that
// matched nothing, an `if true &&` mutant no armed test could see, and a peer
// project's Fired() that returned false for every single-fault mode.
//
// So this asserts that each route produces the errno it claims. Without it, a
// route that started returning EINVAL — which is exactly what Sync and Truncate
// already do on these same devices — would hand every predicate row an error of
// the wrong class, and each row would compare two things that agree about
// nothing in particular.
func TestTheRealErrorControlsProduceTheErrnoTheyClaim(t *testing.T) {
	cases := []struct {
		name  string
		errno syscall.Errno
		op    string
		got   func(*testing.T) error
	}{
		{"Write/ENOSPC", syscall.ENOSPC, "write", func(t *testing.T) error {
			return onFullDevice(t, func(f *os.File) error { _, err := f.Write([]byte("x")); return err })
		}},
		{"WriteAt/ENOSPC", syscall.ENOSPC, "write", func(t *testing.T) error {
			return onFullDevice(t, func(f *os.File) error { _, err := f.WriteAt([]byte("x"), 0); return err })
		}},
		{"Read/EIO", syscall.EIO, "read", func(t *testing.T) error {
			return onUnmappedMemory(t, func(f *os.File) error { _, err := f.ReadAt(make([]byte, 8), 0); return err })
		}},
		{"Write/EIO", syscall.EIO, "write", func(t *testing.T) error {
			return onUnmappedMemory(t, func(f *os.File) error { _, err := f.WriteAt([]byte("xxxxxxxx"), 0); return err })
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.got(t)

			// Nil is the failure that matters most. A control that returns no
			// error reads, at every call site, exactly like an operation that
			// was never going to fail.
			if err == nil {
				t.Fatalf("the control reported no error, so it supplies nothing to compare against")
			}

			var pe *os.PathError
			if !errors.As(err, &pe) {
				t.Fatalf("the control produced %T, want *os.PathError: %v", err, err)
			}
			if pe.Op != tc.op {
				t.Errorf("the control reports Op %q, want %q", pe.Op, tc.op)
			}
			if !errors.Is(err, tc.errno) {
				t.Errorf("the control reports %v, want %v — the route no longer produces "+
					"the error class it is named for, so every predicate row built on it "+
					"is comparing the wrong class", pe.Err, tc.errno)
			}
		})
	}
}

// The routes this file does NOT offer, asserted rather than described.
//
// The doc comment above lists Sync and Truncate as reporting EINVAL on these
// devices, and Close as reporting nothing. A list in a comment goes stale in
// silence. If a future kernel makes Sync on /dev/full report ENOSPC, that is a
// better control than anything here and this test is how anyone finds out.
func TestTheUnreachableRoutesAreStillUnreachable(t *testing.T) {
	requireRealErrors(t)

	unreachable := []struct {
		name  string
		errno syscall.Errno
		got   func(*testing.T) error
	}{
		{"Sync on /dev/full is not ENOSPC", syscall.ENOSPC, func(t *testing.T) error {
			return onFullDevice(t, func(f *os.File) error { return f.Sync() })
		}},
		{"Truncate on /dev/full is not ENOSPC", syscall.ENOSPC, func(t *testing.T) error {
			return onFullDevice(t, func(f *os.File) error { return f.Truncate(0) })
		}},
		{"Sync on /proc/self/mem is not EIO", syscall.EIO, func(t *testing.T) error {
			return onUnmappedMemory(t, func(f *os.File) error { return f.Sync() })
		}},
	}

	for _, tc := range unreachable {
		t.Run(tc.name, func(t *testing.T) {
			if errors.Is(tc.got(t), tc.errno) {
				t.Errorf("this route now reports %v, which the doc comment in this file says "+
					"it does not. That is good news: it is a real control for a method that "+
					"has none. Promote it and correct the comment.", tc.errno)
			}
		})
	}
}
