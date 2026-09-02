package fs_test

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"syscall"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// Contract rule 4 at the File level, which had no check of any kind.
//
// TestInjectedOpStringsMatchTheRealOnes pins the FS level and its doc comment
// says why: "a later tidy-up that makes the Op strings match the method names
// would pass the entire suite while making every injected error a lie about
// what the filesystem does". Nothing said the same thing one level down, and
// the File level is where that exact tidy-up had already happened.
//
// THE OP STRING DOES NOT DEPEND ON THE ERRNO. It names the method the os
// package called, so a real error of ANY class is a correct source for it.
// That matters, because no route reaches EIO or ENOSPC for Sync, Truncate,
// Close or Seek (see realerr_test.go), and without this separation those four
// methods would have no Op check at all.

// onClosedHandle runs do against an *os.File that is already closed, and
// returns what the os package reported.
//
// Every method reports through this route, which is why it is the Op source.
// Nothing here reads its errno: os.ErrClosed is not a class this package
// injects, and a test that compared errnos against it would be comparing two
// things that were never meant to agree.
func onClosedHandle(t *testing.T, do func(*os.File) error) error {
	t.Helper()

	f, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatalf("the Op control cannot create a file, so it compares nothing: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("the Op control cannot close a file, so it compares nothing: %v", err)
	}
	return do(f)
}

// injectedFileError drives one File method through a sweep over a real
// filesystem and returns the error the wrapper injected into it.
//
// The base is faultfs.OS() rather than a stub, because Seek and WriteAt are
// capability-gated: over a base that cannot do them the wrapper refuses with
// errors.ErrUnsupported, which is a different error than the one under test.
//
// The open is operation 1 and the method under test is operation 2, so pass 2
// is the one that fails it. Every pass performs every operation, because
// putting the `continue` first makes the skipped passes perform nothing at all
// and the sweep then terminates on the first one — the trap doc.go records the
// author of this package falling into twice.
func injectedFileError(t *testing.T, newFS func(*fault.Points, faultfs.FS) *faultfs.Fault, call func(faultfs.File) error) error {
	t.Helper()

	dir := t.TempDir()
	var got error
	for n, p := range fault.Sweep(t) {
		f, openErr := newFS(p, faultfs.OS()).OpenFile(filepath.Join(dir, "f"), os.O_RDWR|os.O_CREATE, 0o600)
		if openErr != nil {
			continue
		}
		err := call(f)
		if n == 2 {
			got = err
		}
		_ = f.Close()
	}
	if got == nil {
		t.Fatal("the sweep injected no error into the method under test, so this compares nothing")
	}
	return got
}

func seekOf(f faultfs.File) (interface {
	Seek(int64, int) (int64, error)
}, bool) {
	s, ok := f.(interface {
		Seek(int64, int) (int64, error)
	})
	return s, ok
}

func writeAtOf(f faultfs.File) (interface {
	WriteAt([]byte, int64) (int, error)
}, bool) {
	w, ok := f.(interface {
		WriteAt([]byte, int64) (int, error)
	})
	return w, ok
}

func readAtOf(f faultfs.File) (interface {
	ReadAt([]byte, int64) (int, error)
}, bool) {
	r, ok := f.(interface {
		ReadAt([]byte, int64) (int, error)
	})
	return r, ok
}

// fileOpCase is one File method: the same operation on a real *os.File and on
// the wrapper.
type fileOpCase struct {
	name   string
	onReal func(*os.File) error
	inject func(faultfs.File) error
}

// fileOpCases is package-level so the completeness check below can compare it
// against fs.File. Seek and WriteAt are members of no interface — they are
// capabilities a caller type-asserts for — so reflection cannot see them and
// they are named here by hand. That hole is the one fs/contract_test.go:67
// already records for the operation table, and it is the reason WriteAt could
// carry a wrong Op string with every gate in this repository green.
var fileOpCases = []fileOpCase{
	{"Read",
		func(f *os.File) error { _, err := f.Read(make([]byte, 4)); return err },
		func(f faultfs.File) error { _, err := f.Read(make([]byte, 4)); return err }},
	{"Write",
		func(f *os.File) error { _, err := f.Write([]byte("x")); return err },
		func(f faultfs.File) error { _, err := f.Write([]byte("x")); return err }},
	{"Sync",
		(*os.File).Sync,
		faultfs.File.Sync},
	{"Truncate",
		func(f *os.File) error { return f.Truncate(0) },
		func(f faultfs.File) error { return f.Truncate(0) }},
	{"Close",
		(*os.File).Close,
		faultfs.File.Close},
	{"Seek",
		func(f *os.File) error { _, err := f.Seek(0, io.SeekStart); return err },
		func(f faultfs.File) error {
			s, ok := seekOf(f)
			if !ok {
				return errors.New("the wrapper does not offer Seek")
			}
			_, err := s.Seek(0, io.SeekStart)
			return err
		}},
	{"WriteAt",
		func(f *os.File) error { _, err := f.WriteAt([]byte("x"), 0); return err },
		func(f faultfs.File) error {
			w, ok := writeAtOf(f)
			if !ok {
				return errors.New("the wrapper does not offer WriteAt")
			}
			_, err := w.WriteAt([]byte("x"), 0)
			return err
		}},
	{"ReadAt",
		func(f *os.File) error { _, err := f.ReadAt(make([]byte, 1), 0); return err },
		func(f faultfs.File) error {
			r, ok := readAtOf(f)
			if !ok {
				return errors.New("the wrapper does not offer ReadAt")
			}
			_, err := r.ReadAt(make([]byte, 1), 0)
			return err
		}},
}

// The Op string of every File method, injected against real.
//
// MEASURED 2026-09-01 and this is why it exists: WriteAt injected "writeat"
// and the os package reports "write" for the same call, confirmed by three
// independent routes — a closed handle, /dev/full and /proc/self/mem. Every
// other method already agreed.
//
// The mismatch is the method-name form the FS-level test was written to
// prevent, in the one place no test looked.
func TestInjectedFileOpStringsMatchTheRealOnes(t *testing.T) {
	for _, tc := range fileOpCases {
		t.Run(tc.name, func(t *testing.T) {
			// Same rule as the FS level: on Windows the os package reports the
			// Win32 API it called, and the two Go versions in the matrix
			// disagree with each other, so no value would be right on both.
			if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				t.Skipf("the Op string is platform-specific on %s", runtime.GOOS)
			}

			var realPath *os.PathError
			if !errors.As(onClosedHandle(t, tc.onReal), &realPath) {
				t.Fatalf("the control produced no *os.PathError, so this compares nothing")
			}

			var injPath *os.PathError
			injected := injectedFileError(t, faultfs.New, tc.inject)
			if !errors.As(injected, &injPath) {
				t.Fatalf("the injected error is %T, want *os.PathError: %v", injected, injected)
			}

			if injPath.Op != realPath.Op {
				t.Errorf("injected Op = %q, but the os package reports %q for the same call. "+
					"A caller that switches on Op sees a value no real failure produces.",
					injPath.Op, realPath.Op)
			}
		})
	}
}

// The same completeness check fs/contract_test.go makes, over this table.
//
// fs.File declares five methods. Seek and WriteAt are outside it and outside
// reflection, so this proves the five and says so about the two.
func TestTheFileOpTableCoversTheFileInterface(t *testing.T) {
	iface := reflect.TypeOf((*faultfs.File)(nil)).Elem()

	inTable := make(map[string]bool, len(fileOpCases))
	for _, c := range fileOpCases {
		inTable[c.name] = true
	}

	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		if !inTable[name] {
			t.Errorf("fs.File has %s and the File Op table does not, so nothing proves the "+
				"error it injects carries the Op string the os package really reports", name)
		}
	}

	// The optional capabilities, read from a live handle rather than written
	// down. Reflection over fs.File cannot see them, and a hand-written list
	// here is how WriteAt kept a wrong Op string: the list named the methods
	// the author remembered, and no check compared it to the wrapper.
	dir := t.TempDir()
	f, err := faultfs.New(&fault.Points{}, faultfs.OS()).OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = f.Close() }()
	for _, optional := range optionalMethodsOf(t, f) {
		if !inTable[optional] {
			t.Errorf("the wrapper offers %s beyond fs.File and the File Op table has no row for it, "+
				"so nothing proves the Op string it injects is the one the os package reports", optional)
		}
	}

	if iface.NumMethod() == 0 {
		t.Fatal("reflection reported no methods on fs.File, so this check compared nothing")
	}
}

// predicate is one question a caller asks of an error.
//
// doc.go:69 gives rule 4 its testable form: an injected error must be
// indistinguishable from the real one to EVERY predicate the code under test
// applies to it. Not "an error occurred". These are the questions a store
// actually asks, and doc.go:77 records what the first of them cost a peer
// project when the answer differed.
type predicate struct {
	name string
	ask  func(error) bool
}

var predicates = []predicate{
	{"errors.As(*os.PathError)", func(e error) bool { var t *os.PathError; return errors.As(e, &t) }},
	{"errors.As(*os.LinkError)", func(e error) bool { var t *os.LinkError; return errors.As(e, &t) }},
	{"errors.Is(syscall.EIO)", func(e error) bool { return errors.Is(e, syscall.EIO) }},
	{"errors.Is(syscall.ENOSPC)", func(e error) bool { return errors.Is(e, syscall.ENOSPC) }},
	{"os.IsNotExist", os.IsNotExist},
	{"os.IsPermission", os.IsPermission},
	{"errors.Is(os.ErrClosed)", func(e error) bool { return errors.Is(e, os.ErrClosed) }},
	{"errors.Is(errors.ErrUnsupported)", func(e error) bool { return errors.Is(e, errors.ErrUnsupported) }},
}

// The predicate table at the FS level, over the same rows as the Op table in
// os_test.go, so there is one FS table and not two.
//
// No unprivileged route makes a path operation report EIO or ENOSPC, the two
// classes this package injects: realerr_test.go measured what the two devices
// give each FS method, and the rows here use a missing path or a directory
// under a file, which give ENOENT and ENOTDIR. So a real error of the injected
// class is not available at this level, and the comparison is against a real
// error of a DIFFERENT class.
//
// That does not make the class predicates uncomparable. It makes their
// answers predictable. Each row names, in its differs list, the predicates
// the class difference changes: errors.Is(syscall.EIO) on every row, and
// os.IsNotExist on the ENOENT rows. Those must DISAGREE, or the list is stale.
// Every other predicate must AGREE, which is the full rule 4 comparison for
// six of the eight. An earlier version of this test excluded all five class
// predicates as "differs by construction" and logged them; a reviewer showed
// that errors.Is(ENOSPC), os.IsPermission and errors.Is(os.ErrClosed) answer
// false on both sides of every row, so they agree, and the exclusion left
// real coverage unclaimed while stating a wrong reason for it.
//
// What the comparison catches is real: a Rename that returned *os.PathError
// in place of *os.LinkError, or an FS-level refusal that carried the
// errors.ErrUnsupported sentinel no filesystem produces, fails here and passes
// the Op comparison beside it. The row's errShape in os_test.go checks the
// concrete type too, on the way to the Op string; the two guards overlap on
// purpose, because one asks what the error IS and the other asks what a
// caller's predicates SAY, and a wrapper can satisfy one without the other.
func TestInjectedAndRealFSErrorsAnswerEveryPredicateAsTheClassPredicts(t *testing.T) {
	fixture := newOpFixture(t)

	for _, tc := range opCases {
		t.Run(tc.name, func(t *testing.T) {
			real := tc.real(fixture)
			if real == nil {
				t.Fatalf("the real route for %s produced no error, so this row compares nothing", tc.name)
			}
			injected := injectedFSError(t, tc.inj)

			for _, p := range predicates {
				wantAnswer, gotAnswer := p.ask(real), p.ask(injected)
				if slices.Contains(tc.differs, p.name) {
					if wantAnswer == gotAnswer {
						t.Errorf("%s: listed as differing by construction between a real %s and an "+
							"injected EIO, and both answer %v. The differs list is stale, and a stale "+
							"exclusion hides a comparison", p.name, tc.realClass, wantAnswer)
					}
					continue
				}
				if wantAnswer != gotAnswer {
					t.Errorf("%s: the os package answers %v and the injected error answers %v. "+
						"A caller that applies this predicate takes a different branch against "+
						"the injected error than it would against the real one", p.name, wantAnswer, gotAnswer)
				}
			}
			for _, name := range tc.differs {
				if !slices.ContainsFunc(predicates, func(p predicate) bool { return p.name == name }) {
					t.Errorf("differs names %q, which is not a predicate in the table", name)
				}
			}
		})
	}

	// The positive control, at this level. A bare error built with
	// fmt.Errorf is the shape that cost a peer project a record, and at least
	// one predicate has to separate it from a real path error. If none did,
	// every row above would agree about nothing.
	bare := fmt.Errorf("injected failure on %s", "x")
	real := fixture.real.Remove(fixture.missing)
	separated := false
	for _, p := range predicates {
		if p.ask(real) != p.ask(bare) {
			separated = true
		}
	}
	if !separated {
		t.Fatal("no predicate separates a bare fmt.Errorf from a real path error, so the table above proves nothing")
	}
}

// The full strength of rule 4, for the methods where a real error of the
// injected class is reachable.
//
// realerr_test.go establishes the two routes. They reach Read, Write and
// WriteAt and no further, so this covers three of seven methods and the table
// says which — a row that quietly compares less than its neighbours is the
// defect being removed, not a smaller version of the check.
//
// Sync, Truncate, Close and Seek are covered for Op only, by the test above.
// Closing that gap needs a route to a real EIO for them, and realerr_test.go
// records that none of the obvious ones works: both devices report EINVAL.
func TestInjectedAndRealErrorsAnswerEveryPredicateTheSame(t *testing.T) {
	cases := []struct {
		name   string
		newFS  func(*fault.Points, faultfs.FS) *faultfs.Fault
		inject func(faultfs.File) error
		real   func(*testing.T) error
	}{
		{"Read/EIO", faultfs.New,
			func(f faultfs.File) error { _, err := f.Read(make([]byte, 8)); return err },
			func(t *testing.T) error {
				return onUnmappedMemory(t, func(f *os.File) error { _, err := f.ReadAt(make([]byte, 8), 0); return err })
			}},
		{"Write/EIO", faultfs.New,
			func(f faultfs.File) error { _, err := f.Write([]byte("xxxxxxxx")); return err },
			func(t *testing.T) error {
				return onUnmappedMemory(t, func(f *os.File) error { _, err := f.WriteAt([]byte("xxxxxxxx"), 0); return err })
			}},
		{"WriteAt/EIO", faultfs.New,
			func(f faultfs.File) error {
				w, ok := writeAtOf(f)
				if !ok {
					return errors.New("the wrapper does not offer WriteAt")
				}
				_, err := w.WriteAt([]byte("xxxxxxxx"), 0)
				return err
			},
			func(t *testing.T) error {
				return onUnmappedMemory(t, func(f *os.File) error { _, err := f.WriteAt([]byte("xxxxxxxx"), 0); return err })
			}},
		{"ReadAt/EIO", faultfs.New,
			func(f faultfs.File) error {
				r, ok := readAtOf(f)
				if !ok {
					return errors.New("the wrapper does not offer ReadAt")
				}
				_, err := r.ReadAt(make([]byte, 8), 0)
				return err
			},
			func(t *testing.T) error {
				return onUnmappedMemory(t, func(f *os.File) error { _, err := f.ReadAt(make([]byte, 8), 0); return err })
			}},
		{"Write/ENOSPC", faultfs.NewShortWrite,
			func(f faultfs.File) error { _, err := f.Write([]byte("xxxxxxxx")); return err },
			func(t *testing.T) error {
				return onFullDevice(t, func(f *os.File) error { _, err := f.Write([]byte("x")); return err })
			}},
		{"WriteAt/ENOSPC", faultfs.NewShortWrite,
			func(f faultfs.File) error {
				w, ok := writeAtOf(f)
				if !ok {
					return errors.New("the wrapper does not offer WriteAt")
				}
				_, err := w.WriteAt([]byte("xxxxxxxx"), 0)
				return err
			},
			func(t *testing.T) error {
				return onFullDevice(t, func(f *os.File) error { _, err := f.WriteAt([]byte("x"), 0); return err })
			}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			real := tc.real(t) // skips on a platform without the routes
			injected := injectedFileError(t, tc.newFS, tc.inject)

			for _, p := range predicates {
				wantAnswer, gotAnswer := p.ask(real), p.ask(injected)
				if wantAnswer != gotAnswer {
					t.Errorf("%s: the os package answers %v and the injected error answers %v. "+
						"A caller that applies this predicate takes a different branch against "+
						"the injected error than it would against the real one, which is the "+
						"defect rule 4 exists to prevent.", p.name, wantAnswer, gotAnswer)
				}
			}
		})
	}

	// The positive control. If every predicate answered false for both, the
	// loop above would agree about nothing and report a pass. At least one has
	// to separate an error of this family from an unrelated one.
	unrelated := errors.New("an error from fmt.Errorf, which is what a peer project's fault driver returned")
	real := onUnmappedMemory(t, func(f *os.File) error { _, err := f.ReadAt(make([]byte, 8), 0); return err })
	var separated int
	for _, p := range predicates {
		if p.ask(real) != p.ask(unrelated) {
			separated++
		}
	}
	if separated == 0 {
		t.Fatal("no predicate distinguishes a real filesystem error from a bare errors.New, " +
			"so the comparison above proves nothing")
	}
}

// The capability refusal keeps "writeat", and that is deliberate.
//
// The test above requires the INJECTED Op to match the os package, because a
// caller switching on it must not see a value a real failure never produces.
// The refusal is a different animal: it carries errors.ErrUnsupported, which no
// filesystem produces for this call, so there is nothing for it to be
// indistinguishable from. Its message names the capability the base lacks, and
// "write" would misstate that — the base can write, it cannot write at an
// offset.
//
// A deliberate decision that no test asserts is indistinguishable from an
// accident. This is that assertion, and it is here because the accident it
// resembles was found in this same method on the same day.
func TestTheCapabilityRefusalNamesTheCapability(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.New(&fault.Points{}, plainFS{faultfs.OS()})
	f, err := fsys.OpenFile(filepath.Join(dir, "f"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = f.Close() }()

	// The wrapper counts what it hands out, so this test can use the leak
	// detector this package ships.
	//
	// The first version held the handle open and only WINDOWS objected:
	// TempDir cleanup cannot unlink a file another process still has open,
	// while Linux and macOS remove it without a word. Outstanding makes the
	// same defect portable, and e31c87f records the identical leak found the
	// identical way -- by CI, on the one platform that notices.
	t.Cleanup(func() {
		if n := fsys.Outstanding(); n != 0 {
			t.Errorf("%d handle(s) still open at the end of this test: %v", n, fsys.OpenPaths())
		}
	})

	w, ok := writeAtOf(f)
	if !ok {
		t.Fatal("the wrapper must always OFFER WriteAt; it refuses at run time, not by absence")
	}
	_, err = w.WriteAt([]byte("x"), 0)

	if !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("the refusal is %v, want one satisfying errors.Is(err, errors.ErrUnsupported)", err)
	}

	var pe *os.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("the refusal is %T, want *os.PathError", err)
	}
	// One constant, read by the condition AND by the message. Writing the
	// wanted value twice lets the two drift, and a diagnostic that reports
	// `want "writeat"` from a check for "write" is a test lying about its own
	// condition. Found by running this test's own control.
	const wantOp = "writeat"
	if pe.Op != wantOp {
		t.Errorf("the refusal reports Op %q, want %q. This one does NOT follow the os package: "+
			"it names the capability the base lacks, and \"write\" would say the base cannot "+
			"write at all.", pe.Op, wantOp)
	}
}
