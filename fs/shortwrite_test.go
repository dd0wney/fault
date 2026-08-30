package fs_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// A short write is the second deliberate exception to contract rule 2, and it
// is an exception for a different reason than Close.
//
// Close leaves an effect because POSIX forces it: a failing close(2) releases
// the descriptor regardless. A short write leaves an effect because the effect
// IS the point. A store that assumes a failed write moved no bytes is the
// defect this constructor exists to find, and it cannot be found unless the
// bytes really land.
//
// The bytes on disk must equal the bytes the call reported. A caller that
// resumes from the returned offset has to land on the first byte it did not
// write, or the record is torn in a way no sweep would ever reveal.
func TestAShortWriteMovesTheBytesItReports(t *testing.T) {
	root := t.TempDir()
	want := []byte("the quick brown fox jumps over the lazy dog")

	var (
		short   int
		shortEr error
		onDisk  []byte
	)
	for pass, p := range fault.Sweep(t) {
		path := filepath.Join(root, strconv.Itoa(pass))
		f, openErr := faultfs.NewShortWrite(p, faultfs.OS()).
			OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if openErr != nil {
			continue // this pass failed the open, so no write happens
		}
		n, writeErr := f.Write(want)
		_ = f.Close()
		if writeErr == nil {
			continue
		}
		short, shortEr = n, writeErr
		onDisk, _ = os.ReadFile(path)
	}

	// Positive control. Every assertion below is vacuous if nothing failed.
	if shortEr == nil {
		t.Fatal("no pass failed the write: the sweep injected nothing, so this test proves nothing")
	}

	if short <= 0 || short >= len(want) {
		t.Fatalf("a short write reported %d bytes of %d, want a count in between", short, len(want))
	}
	// The exact count is contract, not an implementation detail, and the reason
	// is the one the constructor gives: a sweep must be reproducible. A caller
	// records a failing pass and runs it again, and a count that drifted
	// between releases would move the record with it.
	if half := len(want) / 2; short != half {
		t.Errorf("a short write moved %d bytes of %d, want %d: the documented count is half the buffer",
			short, len(want), half)
	}
	if !bytes.Equal(onDisk, want[:short]) {
		t.Errorf("the file holds %q, but the call reported %d bytes, which is %q",
			onDisk, short, want[:short])
	}
}

// Contract rule 4: the error must be one a real filesystem would produce. A
// full disk is what makes a real write short, so ENOSPC is what it reports --
// and a store that tells ENOSPC apart from EIO, in order to free space and
// retry, can only be tested if the adapter keeps them apart.
func TestAShortWriteReportsENOSPC(t *testing.T) {
	const name = "x"

	var got error
	for _, p := range fault.Sweep(t) {
		f, openErr := faultfs.NewShortWrite(p, newStub()).OpenFile(name, os.O_RDWR, 0o600)
		if openErr != nil {
			continue
		}
		if _, err := f.Write([]byte("hello")); err != nil {
			got = err
		}
		_ = f.Close()
	}
	if got == nil {
		t.Fatal("no pass failed the write, so this test proves nothing")
	}

	if !errors.Is(got, syscall.ENOSPC) {
		t.Errorf("err = %v, want it to wrap syscall.ENOSPC: a full disk is what shortens a write", got)
	}
	var pathErr *os.PathError
	if !errors.As(got, &pathErr) {
		t.Fatalf("err = %v (%T), want an *os.PathError", got, got)
	}
	if pathErr.Op != "write" {
		t.Errorf("Op = %q, want %q", pathErr.Op, "write")
	}
	if pathErr.Path != name {
		t.Errorf("Path = %q, want %q", pathErr.Path, name)
	}
}

// The shape rule applies to the short write as much as to any other injected
// error, so it is compared against a real one rather than against a belief.
//
// The control is a write to a file opened read-only. That is a measured real
// failure of the write operation, which is what makes it the right control:
// comparing a write against a failed open would compare two different Ops.
func TestAShortWriteHasTheShapeOfARealOne(t *testing.T) {
	realErr := func() error {
		// Create the file, then reopen it read-only. The two steps cannot be
		// merged into one O_CREATE|O_RDONLY open, and the reason is not
		// obvious: on Windows, syscall.Open adds GENERIC_WRITE to the access
		// mask whenever O_CREAT is set, whatever the read/write mode asked
		// for. The handle comes back writable, the control write succeeds, and
		// the control stops being one.
		//
		//	if flag&O_CREAT != 0 { access |= GENERIC_WRITE }
		//	-- GOROOT/src/syscall/syscall_windows.go
		//
		// Linux hid this, because there O_CREAT|O_RDONLY is genuinely
		// read-only. Windows CI found it.
		path := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("preparing the control: %v", err)
		}
		f, err := os.Open(path) // O_RDONLY alone
		if err != nil {
			t.Fatalf("preparing the control: %v", err)
		}
		defer func() { _ = f.Close() }()
		_, writeErr := f.Write([]byte("x"))
		return writeErr
	}()
	if realErr == nil {
		t.Fatal("the control produced no error, so this test compares nothing")
	}

	var injected error
	for _, p := range fault.Sweep(t) {
		f, openErr := faultfs.NewShortWrite(p, newStub()).OpenFile("x", os.O_RDWR, 0o600)
		if openErr != nil {
			continue
		}
		if _, err := f.Write([]byte("hello")); err != nil {
			injected = err
		}
		_ = f.Close()
	}
	if injected == nil {
		t.Fatal("no error was injected, so this test compares nothing")
	}

	for name, pred := range shapePredicates {
		if got, want := pred(injected), pred(realErr); got != want {
			t.Errorf("%q: injected=%v real=%v\n  injected: %v\n  real:     %v",
				name, got, want, injected, realErr)
		}
	}
}

// writeFailFS opens without complaint and then fails every write to the file it
// returns. It is the world saying no partway through, which is not the same
// event as the fault this package chose to inject.
type writeFailFS struct{ err error }

func (w writeFailFS) OpenFile(string, int, os.FileMode) (faultfs.File, error) {
	return writeFailFile{err: w.err}, nil
}
func (writeFailFS) Remove(string) error                   { return nil }
func (writeFailFS) Rename(string, string) error           { return nil }
func (writeFailFS) Stat(string) (os.FileInfo, error)      { return nil, nil }
func (writeFailFS) MkdirAll(string, os.FileMode) error    { return nil }
func (writeFailFS) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }

type writeFailFile struct{ err error }

func (f writeFailFile) Write([]byte) (int, error) { return 0, f.err }
func (writeFailFile) Read([]byte) (int, error)    { return 0, nil }
func (writeFailFile) Sync() error                 { return nil }
func (writeFailFile) Truncate(int64) error        { return nil }
func (writeFailFile) Close() error                { return nil }

// A short write performs a real, partial write, so for the first time an
// injected failure has a base call that can fail on its own. When it does, the
// base error wins.
//
// The reason is the one OpenFile already states: an error from the base is the
// world saying no, and the code under test has to see what the filesystem
// actually said. Reporting ENOSPC over the top of it hands the caller a lie,
// and the caller may free space and retry against a disk that was never full.
func TestAShortWriteDoesNotHideTheBaseError(t *testing.T) {
	want := errors.New("the disk is on fire")

	// The scenario is an open and then a write, so the write is operation 2 and
	// pass 2 is the pass that trips it.
	//
	// Naming the pass is not fussiness. Every pass of this scenario returns
	// want, because the base fails on its own whether or not the sweep injected
	// anything. A loop that kept the last error read the right value from the
	// wrong pass, and passed against an implementation that could not satisfy
	// it.
	const writePass = 2

	var got error
	var reached bool
	for n, p := range fault.Sweep(t) {
		f, openErr := faultfs.NewShortWrite(p, writeFailFS{err: want}).
			OpenFile("x", os.O_RDWR, 0o600)
		if openErr != nil {
			continue
		}
		_, err := f.Write([]byte("hello"))
		if n == writePass {
			got, reached = err, true
		}
		_ = f.Close()
	}
	if !reached {
		t.Fatalf("pass %d never ran, so this test proves nothing", writePass)
	}
	if got == nil {
		t.Fatalf("pass %d returned nil: the base fails every write", writePass)
	}

	if !errors.Is(got, want) {
		t.Errorf("err = %v, want the base's own error: an injected ENOSPC must not cover it", got)
	}
	if errors.Is(got, syscall.ENOSPC) {
		t.Errorf("err = %v: the base failed, so ENOSPC is not what happened", got)
	}
}

// The two constructors must trip at the same points.
//
// A sweep is comparable across the two runs only if the sequence is identical,
// and it is identical only while Write calls Trip once, before the operation,
// under both.
//
// Counting passes does not test that, and an earlier version of this test did
// exactly that and was blind. Under a short write reported as a success the
// scenario runs on to the next operation, the sweep still ends after the same
// number of passes, and the count matches while the behaviour differs. How far
// each pass got is what separates them, so that is what this records.
func TestBothConstructorsVisitTheSamePoints(t *testing.T) {
	// steps reports how many operations the scenario completed before it
	// stopped, the way real code stops at the first error.
	steps := func(fsys faultfs.FS) int {
		f, err := fsys.OpenFile("x", os.O_RDWR, 0o600)
		if err != nil {
			return 0
		}
		defer func() { _ = f.Close() }()
		if _, err := f.Write([]byte("hello")); err != nil {
			return 1
		}
		if err := f.Sync(); err != nil {
			return 2
		}
		return 3
	}

	// The build parameter takes *faultfs.Fault, not faultfs.FS. New and
	// NewShortWrite return the concrete type so Outstanding is reachable, and a
	// function VALUE of theirs is not assignable to one returning the interface.
	// Go has no covariance in a function's result type, so this signature had to
	// change with them.
	trace := func(build func(*fault.Points, faultfs.FS) *faultfs.Fault) []int {
		var reached []int
		for _, p := range fault.Sweep(t) {
			reached = append(reached, steps(build(p, newStub())))
		}
		return reached
	}

	whole, short := trace(faultfs.New), trace(faultfs.NewShortWrite)
	if !slices.Equal(whole, short) {
		t.Errorf("operations completed on each pass differ:\n  New:           %v\n  NewShortWrite: %v",
			whole, short)
	}
}

// NewShortWrite changes the write and nothing else. It is not a full disk: the
// other operations keep the errno New gives them, so a store cannot pass the
// short-write sweep by branching on ENOSPC everywhere.
//
// The subject here is the other operations, not Write. The map named one holds
// OpenFile, Remove, Rename, Stat, MkdirAll and ReadDir, and Write is absent
// from it on purpose: the three tests above already pin what a short write
// returns. This one pins what the rest of the adapter does NOT become.
func TestOnlyTheWriteChangesShape(t *testing.T) {
	for name, op := range one {
		t.Run(name, func(t *testing.T) {
			var got error
			for n, p := range fault.Sweep(t) {
				err := op(faultfs.NewShortWrite(p, newStub()))
				if n == 1 {
					got = err
				}
			}
			if got == nil {
				t.Fatalf("%s returned nil on the failing pass", name)
			}
			if !errors.Is(got, syscall.EIO) {
				t.Errorf("%s = %v, want EIO: NewShortWrite changes Write alone", name, got)
			}
		})
	}
}
