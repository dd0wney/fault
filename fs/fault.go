package fs

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"

	"github.com/dd0wney/fault"
)

// Fault is an FS that fails one operation and counts what it hands out.
//
// New returns this concrete type rather than the FS interface, for the same
// reason alloc.New does: Outstanding is not part of FS and a caller needs it.
// Widening FS instead would break every external implementation to serve a
// method only the wrapper can answer.
type Fault struct {
	mu          sync.Mutex
	outstanding int // handles handed out and not yet returned

	p    *fault.Points
	base FS
	err  error // the errno every injected failure reports
	// shortWrite selects what a tripped Write does. False fails the whole
	// operation and moves no bytes. True writes part of the buffer for real
	// and reports ENOSPC.
	shortWrite bool
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
func New(p *fault.Points, base FS) *Fault { return &Fault{p: p, base: base, err: syscall.EIO} }

// NewShortWrite returns an FS whose failing write is a SHORT write: it moves
// the first half of the buffer for real, then reports ENOSPC.
//
// Half is contract, not an accident. A sweep is a regression test, so a caller
// records a failing pass and runs it again, and a count that drifted between
// releases would move that record with it.
//
// It changes Write and nothing else. Every other operation fails exactly as it
// does under [New], with EIO, so this is a short write and not a full disk.
//
// The two constructors find different defects, so a thorough sweep runs both.
// [New] finds a caller that mishandles a failed write. This finds a caller that
// assumes a failed write moved no bytes, or that ignores the returned count --
// the bug class the package documentation lists as out of reach.
//
// This is the second deliberate exception to contract rule 2, after Close, and
// it is an exception for a different reason. Close leaves an effect because
// POSIX forces it. A short write leaves an effect because the effect is the
// point: the torn record on disk is the state the caller must survive.
func NewShortWrite(p *fault.Points, base FS) *Fault {
	return &Fault{p: p, base: base, err: syscall.EIO, shortWrite: true}
}

// Outstanding reports how many handles have been opened and not closed.
//
// This is the second of the three assertions a fault-injection loop needs: the
// operation failed, nothing leaked, the state is still valid. Drop it and every
// unwind path in the code under test can leak a descriptor while the sweep
// still reports a clean walk. CERT names the class FIO42-C, and it is violated
// almost exclusively on error paths, because the happy path closes.
//
// Two rules make the count truthful, and both are the opposite of the naive
// reading:
//
//   - A FAILED open counts nothing. It handed nothing out, so counting it would
//     report a leak on every pass that fails an open, which is most of them.
//   - A FAILED close still DECREMENTS. POSIX releases the descriptor whether or
//     not close(2) reports an error, and this package's Close closes the real
//     handle either way. Counting it as still held would report a leak the code
//     under test has no way to avoid.
//
// The author of this package needed both rules explained to him by his own
// tests failing on Windows, where four handles left open by its own test suite
// blocked a temporary directory from being removed.
func (f *Fault) Outstanding() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outstanding
}

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
func (f *Fault) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
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
	f.mu.Lock()
	f.outstanding++
	f.mu.Unlock()

	return &faultFile{owner: f, p: f.p, base: file, err: f.err, name: name, shortWrite: f.shortWrite}, nil
}

func (f *Fault) Remove(name string) error {
	if f.p.Trip() {
		return f.fail("remove", name)
	}
	return f.base.Remove(name)
}

// Rename is the one method fail cannot serve. os.Rename reports an
// *os.LinkError, because the operation names two paths, and a store that
// type-switches on the error would take a branch under the sweep that it can
// never take in production. Measured against the os package, not remembered.
func (f *Fault) Rename(oldname, newname string) error {
	if f.p.Trip() {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: f.err}
	}
	return f.base.Rename(oldname, newname)
}

func (f *Fault) Stat(name string) (os.FileInfo, error) {
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
func (f *Fault) MkdirAll(name string, perm os.FileMode) error {
	if f.p.Trip() {
		return f.fail("mkdir", name)
	}
	return f.base.MkdirAll(name, perm)
}

// ReadDir reports Op "open". The real implementation opens the directory
// first, so a failure surfaces as an open rather than as a read.
func (f *Fault) ReadDir(name string) ([]os.DirEntry, error) {
	if f.p.Trip() {
		return nil, f.fail("open", name)
	}
	return f.base.ReadDir(name)
}

// fail builds the error a real filesystem reports for op on name. It is a
// method because it reads f.err, which is what lets the injected errno be a
// property of the run rather than a constant in each method.
func (f *Fault) fail(op, name string) error {
	return &os.PathError{Op: op, Path: name, Err: f.err}
}

// faultFile is an open file whose operations are counted on the same sequence
// as the filesystem that opened it.
type faultFile struct {
	// owner is told when this handle is returned, so Outstanding can report a
	// descriptor leak on an error path.
	owner *Fault

	p    *fault.Points
	base File
	err  error
	// name is captured at OpenFile, because that is the only place it exists.
	// A faultFile without it cannot build a truthful *os.PathError: every
	// os.File method reports the file's own name as Path.
	name string
	// shortWrite is carried down from the faultFS that opened this file, so
	// the file's write behaves the way the constructor promised.
	shortWrite bool
}

func (f *faultFile) Read(b []byte) (int, error) {
	if f.p.Trip() {
		return 0, f.fail("read")
	}
	return f.base.Read(b)
}

// Write fails in one of two ways, chosen by the constructor.
//
// Under New it fails whole: no bytes move, and it reports EIO. Under
// NewShortWrite it moves part of the buffer for real and reports ENOSPC. That
// second form is the exception to contract rule 2, and the effect it leaves
// behind is the whole point of it -- a torn record on disk is the state a
// caller has to survive, and no other injection in this package produces one.
//
// Trip is called once either way, before the operation, so a scenario trips at
// the same point under both constructors and the two sweeps stay comparable.
func (f *faultFile) Write(b []byte) (int, error) {
	if !f.p.Trip() {
		return f.base.Write(b)
	}
	if !f.shortWrite {
		return 0, f.fail("write")
	}

	// Half the buffer. A torn record is what a caller must survive, and half
	// is the most representative shape of one: a record whose header says N
	// bytes and whose body holds fewer.
	//
	// The count is a function of the buffer alone. That is what keeps a sweep
	// reproducible -- a count drawn from a clock or a random source would make
	// a failing pass impossible to run again, which is the one thing a
	// regression test needs from it.
	//
	// A one-byte buffer halves to zero, so a short write of one byte moves
	// nothing. That is truthful, because a full disk really does write zero
	// bytes, and it means the short and whole forms converge at small sizes.
	n, baseErr := f.base.Write(b[:len(b)/2])
	if baseErr != nil {
		// The world said no partway through, and its answer outranks the one
		// this package chose. Both halves of it matter. Reporting ENOSPC here
		// would send a caller to free space on a disk that was never full, and
		// reporting the injected count would claim bytes that never landed.
		return n, baseErr
	}
	return n, f.failWith("write", syscall.ENOSPC)
}

// Seek and WriteAt are OPTIONAL capabilities, discovered at run time.
//
// fs.File carries neither, and it does not gain them: its five methods are the
// contract, and a small contract is what lets this package argue for a place in
// the standard library. So the WRAPPER offers both unconditionally and refuses
// at call time when the base cannot serve them, exactly as a consumer of io
// asserts for io.ReaderFrom rather than requiring it.
//
// A caller reaches them by type assertion:
//
//	if s, ok := file.(interface{ Seek(int64, int) (int64, error) }); ok {
//		off, err := s.Seek(0, io.SeekStart)
//	}
//
// Both are fault points. An operation a sweep cannot fail is a silent hole in
// the injection, and contract rule 3 forbids one.

// errUnsupported builds the refusal for a base that cannot perform op.
//
// It wraps errors.ErrUnsupported, which is not decoration. The Go
// documentation for that sentinel is prescriptive: a method "should instead
// return an error including appropriate context that satisfies
// errors.Is(err, errors.ErrUnsupported)". A package-local sentinel alone leaves
// the reflex idiom returning false, so a caller concludes the operation
// succeeded. That defect was found in a downstream adapter on 2026-08-30 and
// is not repeated here.
func (f *faultFile) errUnsupported(op string) error {
	return &os.PathError{
		Op:   op,
		Path: f.name,
		Err:  fmt.Errorf("the underlying file cannot %s: %w", op, errors.ErrUnsupported),
	}
}

// Seek moves the read and write position, when the base can.
//
// TODO(§12): the body.
//
// The shape, and the three decisions it carries:
//
//  1. Ask the base for the capability FIRST, before Trip. A base that cannot
//     seek must refuse identically whether or not this pass armed the point,
//     or the sweep's arming changes the answer to "can you do this at all".
//  2. Then Trip, and on true return f.fail("seek") — the injected EIO, like
//     every other operation here.
//  3. Otherwise delegate and return what the base returns UNCHANGED. The
//     result is the new absolute offset, and a caller and a recorder both
//     depend on that being the base's own answer rather than arithmetic.
func (f *faultFile) Seek(offset int64, whence int) (int64, error) {
	// The capability question is asked BEFORE Trip, on purpose. A base that
	// cannot seek must answer the same way whether or not this pass armed the
	// point, or the sweep's arming would change what the file can do rather
	// than only whether it succeeds.
	s, ok := f.base.(interface {
		Seek(offset int64, whence int) (int64, error)
	})
	if !ok {
		return 0, f.errUnsupported("seek")
	}
	if f.p.Trip() {
		return 0, f.fail("seek")
	}
	// Returned unchanged. The result is the new offset relative to the start
	// of the file, and both a caller and a recorder depend on that being the
	// base's own answer rather than arithmetic this package performed.
	return s.Seek(offset, whence)
}

// WriteAt writes at an absolute offset, when the base can, without moving the
// file position.
//
// TODO(§12): the body.
//
// The shape, and the four decisions it carries:
//
//  1. Capability first, as in Seek.
//  2. Trip. On false, delegate unchanged.
//  3. On true under New: fail whole. No bytes move, and it reports EIO.
//  4. On true under NewShortWrite: move HALF the buffer AT off, for real, then
//     report ENOSPC — the same rule Write follows, at the given offset. A
//     header backpatched half-way is precisely the torn state this exists to
//     produce, and it is the case a real SSTable writer hits.
//
// Note what does NOT happen: the file position is untouched either way.
// WriteAt carries its own offset, which is why it costs the recorder nothing.
func (f *faultFile) WriteAt(b []byte, off int64) (int, error) {
	w, ok := f.base.(interface {
		WriteAt(p []byte, off int64) (int, error)
	})
	if !ok {
		return 0, f.errUnsupported("writeat")
	}
	if !f.p.Trip() {
		return w.WriteAt(b, off)
	}
	if !f.shortWrite {
		return 0, f.fail("writeat")
	}

	// Half the buffer, at the offset the caller gave. The same rule Write
	// follows, and for the same reason: a record whose header says N bytes and
	// whose body holds fewer is the state a caller must survive, and half is
	// the most representative shape of one.
	//
	// This form matters more here than it does for Write. A store that
	// backpatches a header by writing at offset 0 after writing the body puts
	// its most important bytes through exactly this call, and a half-written
	// header is the state that reads as valid and is not.
	n, baseErr := w.WriteAt(b[:len(b)/2], off)
	if baseErr != nil {
		// The world said no partway through, and its answer outranks the one
		// this package chose. Reporting ENOSPC here would send a caller to
		// free space on a disk that was never full, and reporting the injected
		// count would claim bytes that never landed.
		return n, baseErr
	}
	return n, f.failWith("writeat", syscall.ENOSPC)
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

	// Decrement whether or not the close reported an error, and whether or not
	// this pass injected one. POSIX releases the descriptor either way, and the
	// real handle above was closed either way, so holding the count open would
	// report a leak the caller cannot avoid.
	f.owner.mu.Lock()
	f.owner.outstanding--
	f.owner.mu.Unlock()

	if tripped {
		return f.fail("close")
	}
	return closeErr
}

func (f *faultFile) fail(op string) error { return f.failWith(op, f.err) }

// failWith is fail with the errno named at the call site, and it exists for the
// one operation whose errno is not f.err.
//
// A short write reports ENOSPC, because a full disk is what makes a real write
// short. Contract rule 4 is why that distinction is kept: a store that responds
// to ENOSPC by freeing space and retrying takes a branch it can never take on
// EIO, and an adapter that reported EIO here would leave that branch untested
// while appearing to cover the write.
func (f *faultFile) failWith(op string, err error) error {
	return &os.PathError{Op: op, Path: f.name, Err: err}
}
