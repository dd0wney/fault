package fs

import (
	"os"
	"syscall"

	"github.com/dd0wney/fault"
)

type faultFS struct {
	p    *fault.Points
	base FS
	err  error // the errno every injected failure reports
}

// New returns an FS that fails one operation, chosen by p, with syscall.EIO.
//
// It wraps base rather than replacing it, so every operation the sweep does not
// fail is served for real and the code under test exercises the real path.
//
// EIO is the default because fault injection wants errors a caller cannot work
// around. A logical error -- ENOENT from an open, EEXIST from a mkdir -- is not
// a fault: a correct store responds to those by succeeding at what it was
// trying to do, so injecting one tests a different path than the one intended.
func New(p *fault.Points, base FS) FS { return &faultFS{p: p, base: base, err: syscall.EIO} }

// OpenFile is the only method that returns another interface, and so the only
// place the injection can leak. The file it hands back must be wrapped, or the
// five operations available on an open file become invisible to the sweep --
// and those are where the interesting failure points live, between a write and
// its sync, and on the close after a failed sync.
//
// The two error paths are not the same and must not be merged. A tripped Trip
// is a fault this package chose. An error from base is the world saying no, and
// it passes through untouched: the code under test has to see what the
// filesystem actually said.
func (f *faultFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	if f.p.Trip() {
		return nil, f.fail("open", name)
	}
	file, openErr := f.base.OpenFile(name, flag, perm)
	if openErr != nil {
		return nil, openErr
	}
	// The same Points, deliberately. The file's operations continue the
	// sequence the filesystem started, so an open, a write, a sync and a close
	// are operations 1 to 4 of one scenario rather than two separate counts.
	return &faultFile{p: f.p, base: file, err: f.err, name: name}, nil
}

func (f *faultFS) Remove(name string) error {
	if f.p.Trip() {
		return f.fail("remove", name)
	}
	return f.base.Remove(name)
}

// Rename is the one method fail cannot serve. os.Rename reports an
// *os.LinkError, because the operation names two paths, and a store that
// type-switches on the error would take a branch under the sweep that it can
// never take in production. Measured against the os package, not remembered.
func (f *faultFS) Rename(oldname, newname string) error {
	if f.p.Trip() {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: f.err}
	}
	return f.base.Rename(oldname, newname)
}

func (f *faultFS) Stat(name string) (os.FileInfo, error) {
	if f.p.Trip() {
		return nil, f.fail("stat", name)
	}
	return f.base.Stat(name)
}

// MkdirAll reports Op "mkdir", not "mkdirall": the real os.MkdirAll surfaces
// the failing syscall, not the helper that called it.
//
// Known limitation. A real os.MkdirAll may create several directories before
// one fails, leaving a partial tree behind. This fails before doing anything,
// so it never reproduces that state. A store that cleans up after a partial
// mkdir has no coverage of that path from this adapter.
func (f *faultFS) MkdirAll(name string, perm os.FileMode) error {
	if f.p.Trip() {
		return f.fail("mkdir", name)
	}
	return f.base.MkdirAll(name, perm)
}

// ReadDir reports Op "open". The real implementation opens the directory
// first, so a failure surfaces as an open rather than as a read.
func (f *faultFS) ReadDir(name string) ([]os.DirEntry, error) {
	if f.p.Trip() {
		return nil, f.fail("open", name)
	}
	return f.base.ReadDir(name)
}

// fail builds the error a real filesystem reports for op on name. It is a
// method because it reads f.err, which is what lets the injected errno be a
// property of the run rather than a constant in each method.
func (f *faultFS) fail(op, name string) error {
	return &os.PathError{Op: op, Path: name, Err: f.err}
}

// faultFile is an open file whose operations are counted on the same sequence
// as the filesystem that opened it.
type faultFile struct {
	p    *fault.Points
	base File
	err  error
	// name is captured at OpenFile, because that is the only place it exists.
	// A faultFile without it cannot build a truthful *os.PathError: every
	// os.File method reports the file's own name as Path.
	name string
}

func (f *faultFile) Read(b []byte) (int, error) {
	if f.p.Trip() {
		return 0, f.fail("read")
	}
	return f.base.Read(b)
}

func (f *faultFile) Write(b []byte) (int, error) {
	if f.p.Trip() {
		return 0, f.fail("write")
	}
	return f.base.Write(b)
}

func (f *faultFile) Sync() error {
	if f.p.Trip() {
		return f.fail("sync")
	}
	return f.base.Sync()
}

func (f *faultFile) Truncate(size int64) error {
	if f.p.Trip() {
		return f.fail("truncate")
	}
	return f.base.Truncate(size)
}

// Close is the exception to contract rule 2, and the exception is deliberate.
//
// Rule 2 exists so that a failed operation leaves no effect the caller can
// observe. Releasing a descriptor is not such an effect -- it is the operating
// system reclaiming its own -- and a real failing close(2) releases it anyway,
// which POSIX guarantees. So Close must report the injected error AND close the
// real handle.
//
// The cost of getting this wrong appears far from the cause. The sweep opens a
// file on every pass and runs up to 512 of them, so a Close that returns early
// leaks one descriptor per pass. Against a real filesystem the run dies partway
// through with "too many open files", and the blame lands on the code under
// test.
//
// The decision is held in a local rather than acted on immediately, which is
// why this method looks unlike its four neighbours. Trip still comes first, so
// the close keeps its place in the sequence; only the returned error depends on
// whether the sweep chose this point. TestAFailedCloseStillReleasesTheHandle is
// the gate.
func (f *faultFile) Close() error {
	tripped := f.p.Trip()
	closeErr := f.base.Close()
	if tripped {
		return f.fail("close")
	}
	return closeErr
}

func (f *faultFile) fail(op string) error {
	return &os.PathError{Op: op, Path: f.name, Err: f.err}
}
