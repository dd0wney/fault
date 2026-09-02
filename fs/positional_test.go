package fs_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"syscall"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// seeker is what a caller type-asserts for. fs.File does not carry Seek, so
// the capability is discovered at runtime, exactly as io.ReaderFrom is.
type seeker interface {
	Seek(offset int64, whence int) (int64, error)
}

type writerAt interface {
	WriteAt(p []byte, off int64) (int, error)
}

type readerAt interface {
	ReadAt(p []byte, off int64) (int, error)
}

// optionalMethodsOf is THE AUTHORITY for the optional method set: the methods
// the wrapper's file offers beyond fs.File, read from a live handle by
// reflection rather than written down. Every table in this package that names
// an optional method is compared against it, so a method added to the wrapper
// and to no table is caught, and so is a table row for a method that is gone.
//
// This replaced a hand-written list, which is the shape fs/contract_test.go
// exists to remove: a list a person maintains is a claim with the same expiry
// as any other. crash/contract_test.go computes the same set from the
// recorder's handle and requires the two to be equal.
func optionalMethodsOf(t *testing.T, f faultfs.File) []string {
	t.Helper()
	iface := reflect.TypeOf((*faultfs.File)(nil)).Elem()
	concrete := reflect.TypeOf(f)
	var out []string
	for i := range concrete.NumMethod() {
		name := concrete.Method(i).Name
		if _, isMember := iface.MethodByName(name); !isMember {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("the wrapper's file offers no method beyond fs.File, so every check of the optional set compares nothing")
	}
	return out
}

// optionalOp is one optional method driven as a fault point.
type optionalOp struct {
	name string
	call func(faultfs.File) error
}

// optionalOps is package-level so TestTheOptionalTableIsTheWrapperOptionalSet
// can compare it against the authority above.
var optionalOps = []optionalOp{
	{"Seek", func(f faultfs.File) error {
		s, ok := f.(seeker)
		if !ok {
			return errNoCapability
		}
		_, err := s.Seek(0, io.SeekStart)
		return err
	}},
	{"WriteAt", func(f faultfs.File) error {
		w, ok := f.(writerAt)
		if !ok {
			return errNoCapability
		}
		_, err := w.WriteAt([]byte("x"), 0)
		return err
	}},
	{"ReadAt", func(f faultfs.File) error {
		r, ok := f.(readerAt)
		if !ok {
			return errNoCapability
		}
		_, err := r.ReadAt(make([]byte, 1), 0)
		return err
	}},
}

// The table above and the wrapper must agree exactly, in both directions.
func TestTheOptionalTableIsTheWrapperOptionalSet(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())
	f := openThrough(t, fsys, dir)

	want := optionalMethodsOf(t, f)
	var got []string
	for _, op := range optionalOps {
		got = append(got, op.name)
	}
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Errorf("the optional table drives %v and the wrapper offers %v beyond fs.File. "+
			"A method in one and not the other is either a fault point no test drives "+
			"or a table row for a method that is gone", got, want)
	}
}

// A base that cannot do positional I/O. os.File can, so a test needs one that
// cannot in order to reach the refusal at all.
type plainFS struct{ base faultfs.FS }

func (p plainFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	f, err := p.base.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return plainFile{f}, nil
}
func (p plainFS) Remove(name string) error                     { return p.base.Remove(name) }
func (p plainFS) Rename(o, n string) error                     { return p.base.Rename(o, n) }
func (p plainFS) Stat(name string) (os.FileInfo, error)        { return p.base.Stat(name) }
func (p plainFS) MkdirAll(name string, perm os.FileMode) error { return p.base.MkdirAll(name, perm) }
func (p plainFS) ReadDir(name string) ([]os.DirEntry, error)   { return p.base.ReadDir(name) }

// plainFile deliberately exposes ONLY the five methods of fs.File, so it
// satisfies neither seeker nor writerAt.
type plainFile struct{ f faultfs.File }

func (p plainFile) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p plainFile) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p plainFile) Sync() error                 { return p.f.Sync() }
func (p plainFile) Truncate(n int64) error      { return p.f.Truncate(n) }
func (p plainFile) Close() error                { return p.f.Close() }

// errNoCapability lets a test REPORT a missing capability instead of panicking
// on a failed type assertion. A test that crashes says less than one that
// names what is absent.
var errNoCapability = errors.New("the wrapper does not offer the capability")

func openThrough(t *testing.T, fsys faultfs.FS, dir string) faultfs.File {
	t.Helper()
	f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// The wrapper offers the capability when the base has it. os.File does.
func TestTheWrapperOffersTheOptionalMethodsOverARealFile(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())
	f := openThrough(t, fsys, dir)

	if _, ok := f.(seeker); !ok {
		t.Error("the wrapped file does not offer Seek, so a caller cannot reach it over an os.File")
	}
	if _, ok := f.(writerAt); !ok {
		t.Error("the wrapped file does not offer WriteAt, so a caller cannot reach it over an os.File")
	}
	if _, ok := f.(readerAt); !ok {
		t.Error("the wrapped file does not offer ReadAt, so a caller cannot reach it over an os.File")
	}
}

// When the base cannot do it, the refusal must satisfy the STANDARD idiom.
// A package-local sentinel alone is the defect this rule exists to avoid: a
// caller writing errors.Is(err, errors.ErrUnsupported) gets false and
// concludes the operation succeeded.
func TestARefusalSatisfiesTheStandardUnsupportedSentinel(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.New(&fault.Points{}, plainFS{faultfs.OS()})
	f := openThrough(t, fsys, dir)

	s, ok := f.(seeker)
	if !ok {
		t.Fatal("the wrapper must always OFFER Seek; it refuses at call time, not by absence")
	}
	got, err := s.Seek(0, io.SeekStart)
	if err == nil {
		t.Fatal("Seek over a base that cannot seek returned no error")
	}
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("Seek error does not satisfy errors.Is(err, errors.ErrUnsupported): %v", err)
	}
	// The VALUE matters as much as the error. A caller that reads the offset
	// from a refused Seek must not be handed a plausible one.
	if got != 0 {
		t.Errorf("a refused Seek returned offset %d, want 0", got)
	}

	w, ok := f.(writerAt)
	if !ok {
		t.Fatal("the wrapper must always OFFER WriteAt")
	}
	n, err := w.WriteAt([]byte("x"), 0)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("WriteAt error does not satisfy errors.Is(err, errors.ErrUnsupported): %v", err)
	}
	if n != 0 {
		t.Errorf("a refused WriteAt reported %d bytes written, want 0", n)
	}

	r, ok := f.(readerAt)
	if !ok {
		t.Fatal("the wrapper must always OFFER ReadAt")
	}
	n, err = r.ReadAt(make([]byte, 1), 0)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("ReadAt error does not satisfy errors.Is(err, errors.ErrUnsupported): %v", err)
	}
	if n != 0 {
		t.Errorf("a refused ReadAt reported %d bytes read, want 0", n)
	}
}

// Every optional method is a fault point. An operation the sweep cannot fail
// is a silent hole in the injection, which the adapter contract forbids.
func TestTheOptionalMethodsAreFaultPoints(t *testing.T) {
	for _, c := range optionalOps {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			var tripped bool
			for n, p := range fault.Sweep(t) {
				fsys := faultfs.New(p, faultfs.OS())
				f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
				if err != nil {
					continue // the open was the armed point
				}
				err = c.call(f)
				_ = f.Close()
				if errors.Is(err, errNoCapability) {
					t.Fatalf("op %d: the wrapper does not offer %s at all", n, c.name)
				}
				if err != nil && errors.Is(err, syscall.EIO) {
					tripped = true
				}
			}
			if !tripped {
				t.Errorf("%s never failed under a sweep, so it is invisible to the injection", c.name)
			}
		})
	}
}

// Under NewShortWrite, a tripped WriteAt follows the same rule Write does:
// half the buffer lands AT THE GIVEN OFFSET, then ENOSPC. A backpatched
// header that lands half-written is the state this exists to produce.
func TestAShortWriteAtMovesHalfTheBufferAtTheOffset(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")

	if err := os.WriteFile(name, []byte("ORIGINALCONTENT!"), 0o600); err != nil {
		t.Fatal(err)
	}

	var short bool

	// Drive the armed point through a sweep so the WriteAt is the operation
	// that trips.
	for n, p := range fault.Sweep(t) {
		fsys := faultfs.NewShortWrite(p, faultfs.OS())
		f, err := fsys.OpenFile(name, os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		w, ok := f.(writerAt)
		if !ok {
			_ = f.Close()
			t.Fatal("the wrapper does not offer WriteAt")
		}
		got, werr := w.WriteAt([]byte("ABCD"), 4)
		_ = f.Close()
		if werr != nil && errors.Is(werr, syscall.ENOSPC) {
			short = true
			if got != 2 {
				t.Errorf("op %d: short WriteAt moved %d bytes, want 2 (half of 4)", n, got)
			}
			on, rerr := os.ReadFile(name)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if string(on[4:6]) != "AB" {
				t.Errorf("op %d: bytes at the offset are %q, want %q", n, on[4:6], "AB")
			}
			// "ORIGINALCONTENT!" holds "AL" at 6 and 7. The short write ends
			// at 6, so those bytes must be untouched: a short write moves
			// what it moved and nothing beyond it.
			if string(on[6:8]) != "AL" {
				t.Errorf("op %d: bytes past the short write are %q, want the original %q", n, on[6:8], "AL")
			}
		}
	}
	if !short {
		t.Error("no pass produced a short WriteAt, so the rule is untested")
	}
}

// The injected failures must report ZERO moved, not merely an error. A caller
// that trusts the count from a failed positional write writes past bytes that
// never landed, and this package exists to catch exactly that class of
// mistake in ITS callers -- so it must not commit it itself.
func TestAnInjectedOptionalMethodReportsZero(t *testing.T) {
	dir := t.TempDir()
	var sawSeek, sawWriteAt, sawReadAt bool

	for n, p := range fault.Sweep(t) {
		fsys := faultfs.New(p, faultfs.OS())
		f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			continue
		}

		s, ok := f.(seeker)
		if !ok {
			_ = f.Close()
			t.Fatalf("op %d: the wrapped file does not offer the seeker interface", n)
		}
		if got, err := s.Seek(4, io.SeekStart); errors.Is(err, syscall.EIO) {
			sawSeek = true
			if got != 0 {
				t.Errorf("op %d: an injected Seek returned offset %d, want 0", n, got)
			}
		}
		w, ok := f.(writerAt)
		if !ok {
			_ = f.Close()
			t.Fatalf("op %d: the wrapped file does not offer the writerAt interface", n)
		}
		if got, err := w.WriteAt([]byte("xyz"), 0); errors.Is(err, syscall.EIO) {
			sawWriteAt = true
			if got != 0 {
				t.Errorf("op %d: an injected WriteAt reported %d bytes, want 0", n, got)
			}
		}
		r, ok := f.(readerAt)
		if !ok {
			_ = f.Close()
			t.Fatalf("op %d: the wrapped file does not offer the readerAt interface", n)
		}
		if got, err := r.ReadAt(make([]byte, 3), 0); errors.Is(err, syscall.EIO) {
			sawReadAt = true
			if got != 0 {
				t.Errorf("op %d: an injected ReadAt reported %d bytes, want 0", n, got)
			}
		}
		_ = f.Close()
	}

	if !sawSeek {
		t.Error("no pass injected a Seek failure, so its count is untested")
	}
	if !sawWriteAt {
		t.Error("no pass injected a WriteAt failure, so its count is untested")
	}
	if !sawReadAt {
		t.Error("no pass injected a ReadAt failure, so its count is untested")
	}
}

// The pass-through path must read at the offset, report what it read, and
// leave the handle position where it was. A store that reads a page at an
// absolute offset and then appends depends on all three.
func TestAnUninjectedReadAtReadsAndDoesNotMoveThePosition(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	fsys := faultfs.New(&fault.Points{}, faultfs.OS())
	f, err := fsys.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r, ok := f.(readerAt)
	if !ok {
		t.Fatal("the wrapper does not offer the readerAt interface")
	}
	buf := make([]byte, 2)
	got, err := r.ReadAt(buf, 4)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if got != 2 || string(buf) != "45" {
		t.Errorf("ReadAt read %d bytes %q, want 2 bytes %q", got, buf, "45")
	}

	// The position is untouched: a sequential read after it starts at 0.
	seq := make([]byte, 2)
	if _, err := f.Read(seq); err != nil {
		t.Fatalf("Read after ReadAt: %v", err)
	}
	if string(seq) != "01" {
		t.Errorf("the read after a ReadAt returned %q, want %q — ReadAt moved the position", seq, "01")
	}
}

// The pass-through path must actually write, and report what it wrote.
func TestAnUninjectedWriteAtWritesAndReportsTheCount(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	fsys := faultfs.New(&fault.Points{}, faultfs.OS())
	f, err := fsys.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := f.(writerAt)
	if !ok {
		_ = f.Close()
		t.Fatal("the wrapper does not offer the writerAt interface")
	}
	got, err := w.WriteAt([]byte("AB"), 4)
	if err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if got != 2 {
		t.Errorf("WriteAt reported %d bytes, want 2", got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	on, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(on) != "0123AB6789" {
		t.Errorf("on disk = %q, want %q", on, "0123AB6789")
	}
}

// failingWriteAtFS has WriteAt and refuses it. plainFS lacks the method
// entirely, which reaches the unsupported path; this one reaches the path
// where the BASE says no partway through an injected short write.
type failingWriteAtFS struct{ base faultfs.FS }

func (p failingWriteAtFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	f, err := p.base.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return failingWriteAtFile{f}, nil
}
func (p failingWriteAtFS) Remove(name string) error              { return p.base.Remove(name) }
func (p failingWriteAtFS) Rename(o, n string) error              { return p.base.Rename(o, n) }
func (p failingWriteAtFS) Stat(name string) (os.FileInfo, error) { return p.base.Stat(name) }
func (p failingWriteAtFS) MkdirAll(name string, perm os.FileMode) error {
	return p.base.MkdirAll(name, perm)
}
func (p failingWriteAtFS) ReadDir(name string) ([]os.DirEntry, error) { return p.base.ReadDir(name) }

type failingWriteAtFile struct{ f faultfs.File }

func (p failingWriteAtFile) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p failingWriteAtFile) Write(b []byte) (int, error) { return p.f.Write(b) }
func (p failingWriteAtFile) Sync() error                 { return p.f.Sync() }
func (p failingWriteAtFile) Truncate(n int64) error      { return p.f.Truncate(n) }
func (p failingWriteAtFile) Close() error                { return p.f.Close() }
func (p failingWriteAtFile) WriteAt(b []byte, off int64) (int, error) {
	return 1, syscall.EROFS // one byte landed, then the world said no
}

// When the BASE fails partway through an injected short WriteAt, its answer
// outranks the one this package chose.
//
// Both halves matter. Reporting ENOSPC would send a caller to free space on a
// disk that was never full. Reporting the injected count would claim bytes
// that never landed.
func TestAShortWriteAtLetsTheBaseErrorOutrankTheInjectedOne(t *testing.T) {
	dir := t.TempDir()
	var reached bool

	for n, p := range fault.Sweep(t) {
		fsys := faultfs.NewShortWrite(p, failingWriteAtFS{faultfs.OS()})
		f, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		w, ok := f.(writerAt)
		if !ok {
			_ = f.Close()
			t.Fatalf("op %d: the wrapper does not offer the writerAt interface", n)
		}
		got, werr := w.WriteAt([]byte("ABCD"), 0)
		_ = f.Close()

		// The assertion is the INVERSE, and it has to be.
		//
		// On a pass where Trip does not fire, the pass-through returns the
		// base's EROFS. On the armed pass, the short-write path calls the base
		// and the base fails too. Both return (1, EROFS), so they are
		// observationally identical and "did I see EROFS" cannot tell them
		// apart — an earlier version of this test asserted exactly that and
		// passed against the defect.
		//
		// What discriminates: this base ALWAYS fails, so a correct
		// implementation can never surface the injected ENOSPC on any pass.
		// If it appears, the base's answer was displaced.
		if errors.Is(werr, syscall.ENOSPC) {
			t.Errorf("op %d: the injected ENOSPC displaced the base's own error", n)
		}
		if errors.Is(werr, syscall.EROFS) {
			reached = true
			if got != 1 {
				t.Errorf("op %d: reported %d bytes, want the base's count of 1", n, got)
			}
		}
	}

	if !reached {
		t.Error("no pass saw the base's error at all, so the rule is untested")
	}
}
