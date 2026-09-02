package crash_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

type seeker interface {
	Seek(offset int64, whence int) (int64, error)
}

type writerAt interface {
	WriteAt(p []byte, off int64) (int, error)
}

type readerAt interface {
	ReadAt(p []byte, off int64) (int, error)
}

// A ReadAt takes an index and does not move the handle position, so the next
// sequential write is recorded where it lands. The two halves are one test
// because a ReadAt that moved the position would put a later write at the
// wrong offset in the record, and the replay control would then disagree
// with the disk for a reason that reads like the store's fault.
func TestAReadAtTakesAnIndexAndDoesNotMoveThePosition(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := f.(readerAt)
	if !ok {
		t.Fatal("the recorder does not offer ReadAt over an os.File")
	}
	before := len(crash.Entries(rec))
	buf := make([]byte, 2)
	if n, err := r.ReadAt(buf, 4); err != nil || n != 2 || string(buf) != "45" {
		t.Fatalf("ReadAt = %d, %q, %v; want 2, %q, nil", n, buf, err, "45")
	}
	if got := len(crash.Entries(rec)); got != before+1 {
		t.Errorf("ReadAt recorded %d entries, want 1, so every later index shifts", got-before)
	}
	if _, err := f.Write([]byte("XY")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var offs []int64
	for _, e := range crash.Entries(rec) {
		if e.Kind == "write" {
			offs = append(offs, e.Off)
		}
	}
	if len(offs) != 1 || offs[0] != 0 {
		t.Errorf("the write after a ReadAt is recorded at %v, want [0] — ReadAt must not move the position", offs)
	}
}

// A seek moves the position, so the NEXT write is recorded where it actually
// landed. Spec §5.2 argued that tracking the offset by addition was sound
// only because fs.File had no Seek. It now has one optionally, and the rule
// becomes: the offset is the last seek result plus the bytes moved since.
func TestASeekMovesWhereTheNextWriteIsRecorded(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := f.(seeker)
	if !ok {
		t.Fatal("the recorder does not offer Seek over an os.File")
	}
	if _, err := s.Seek(4, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("XY")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var wrote []crash.Entry
	for _, e := range crash.Entries(rec) {
		if e.Kind == "write" {
			wrote = append(wrote, e)
		}
	}
	if len(wrote) != 1 {
		t.Fatalf("got %d write entries, want 1", len(wrote))
	}
	if wrote[0].Off != 4 {
		t.Errorf("the write after a seek is recorded at offset %d, want 4", wrote[0].Off)
	}
}

// A WriteAt records its OWN offset and does not move the handle position.
func TestAWriteAtRecordsItsOffsetAndDoesNotMoveThePosition(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := f.(writerAt)
	if !ok {
		t.Fatal("the recorder does not offer WriteAt over an os.File")
	}
	if _, err := w.WriteAt([]byte("AB"), 6); err != nil {
		t.Fatal(err)
	}
	// The position is untouched, so this sequential write lands at 0.
	if _, err := f.Write([]byte("Z")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var offs []int64
	for _, e := range crash.Entries(rec) {
		if e.Kind == "write" {
			offs = append(offs, e.Off)
		}
	}
	if len(offs) != 2 {
		t.Fatalf("got %d write entries, want 2", len(offs))
	}
	if offs[0] != 6 {
		t.Errorf("the WriteAt is recorded at offset %d, want 6", offs[0])
	}
	if offs[1] != 0 {
		t.Errorf("the sequential write after a WriteAt is recorded at offset %d, want 0 — WriteAt must not move the position", offs[1])
	}
}

// The control is what catches a wrong offset, and it must still hold for a
// scenario that seeks and backpatches. This is the header-backpatch shape:
// write a body, seek to 0, rewrite the header.
func TestTheControlHoldsForABackpatchedHeader(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "sst")

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("HDR?body-bytes-here")); err != nil {
		t.Fatal(err)
	}
	s, ok := f.(seeker)
	if !ok {
		t.Fatal("no Seek")
	}
	if _, err := s.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("HDR!")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Plan runs checkReplay first. If the recorded offsets were wrong, the
	// replay would not reproduce the directory and this would fail loudly.
	if _, err := crash.Plan(rec, crash.Model{}); err != nil {
		t.Fatalf("the whole-record control rejected a seek-and-backpatch scenario: %v", err)
	}
}

// noPositionalFS hands back a File offering only the five methods of
// faultfs.File. Embedding the interface is what removes Seek and WriteAt: the
// method set of noPositionalFile is the interface's, whatever the dynamic
// value beneath it can do.
//
// It exists so the refusal path can be reached at all. Over faultfs.OS() the
// assertions inside Seek and WriteAt always succeed, so every test written
// against a real filesystem exercises the branch that works.
type noPositionalFS struct {
	faultfs.FS
}

func (n noPositionalFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	f, err := n.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return noPositionalFile{f}, nil
}

type noPositionalFile struct {
	faultfs.File
}

// A refusal carries a count as well as an error, and the count is read first.
//
// This is coupling row C7 on the recorder's side of the boundary: "a caller
// acts on the count first, and under a short write it is neither 0 nor the
// whole buffer". C7 named the hazard for fault/fs and PR #2 asserted it there.
// The same feature crossed into this recorder and the assertion did not follow,
// so a refusal could report any count at all and every test stayed green.
func TestARefusedSeekOrWriteAtReportsZeroBytes(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(noPositionalFS{faultfs.OS()}, dir)

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	s, ok := f.(seeker)
	if !ok {
		t.Fatal("the recorder offers no Seek, so its refusal cannot be reached")
	}
	got, err := s.Seek(4, io.SeekStart)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a refused Seek gives %v, want an error wrapping errors.ErrUnsupported", err)
	}
	if got != 0 {
		t.Errorf("a refused Seek reports offset %d, want 0", got)
	}

	w, ok := f.(writerAt)
	if !ok {
		t.Fatal("the recorder offers no WriteAt, so its refusal cannot be reached")
	}
	n, err := w.WriteAt([]byte("AB"), 0)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a refused WriteAt gives %v, want an error wrapping errors.ErrUnsupported", err)
	}
	if n != 0 {
		t.Errorf("a refused WriteAt reports %d bytes, want 0", n)
	}

	r, ok := f.(readerAt)
	if !ok {
		t.Fatal("the recorder offers no ReadAt, so its refusal cannot be reached")
	}
	n, err = r.ReadAt(make([]byte, 2), 0)
	if !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("a refused ReadAt gives %v, want an error wrapping errors.ErrUnsupported", err)
	}
	if n != 0 {
		t.Errorf("a refused ReadAt reports %d bytes, want 0", n)
	}
}

// The record must hold the bytes a positional write moved, not a buffer of the
// right length.
//
// Entries exposes Data for exactly this, and its own comment says so: "a write
// records only the bytes that landed". No test asserted it for WriteAt, so the
// copy that fills the entry could be deleted outright with nothing to notice --
// and every rebuilt state is made from that data.
func TestAWriteAtRecordsTheBytesItMoved(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := f.(writerAt)
	if !ok {
		t.Fatal("the recorder does not offer WriteAt over an os.File")
	}
	if _, err := w.WriteAt([]byte("AB"), 6); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var writes []crash.Entry
	for _, e := range crash.Entries(rec) {
		if e.Kind == "write" {
			writes = append(writes, e)
		}
	}
	if len(writes) != 1 {
		t.Fatalf("got %d write entries, want 1", len(writes))
	}
	if !bytes.Equal(writes[0].Data, []byte("AB")) {
		t.Errorf("the WriteAt recorded %q, want %q", writes[0].Data, "AB")
	}
}

// The control must hold for a positional backpatch, not only a seeking one.
//
// TestTheControlHoldsForABackpatchedHeader covers the same shape with Seek and
// Write, and it was the only test driving the whole-record replay. So the
// replay had never been run against a record containing a WriteAt at all.
func TestTheControlHoldsForAWriteAtBackpatch(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "sst")

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("HDR?body-bytes-here")); err != nil {
		t.Fatal(err)
	}
	w, ok := f.(writerAt)
	if !ok {
		t.Fatal("no WriteAt")
	}
	// Backpatch the header in place, which is what a positional write is for.
	if _, err := w.WriteAt([]byte("HDR!"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := crash.Plan(rec, crash.Model{}); err != nil {
		t.Fatalf("the whole-record control rejected a WriteAt backpatch: %v", err)
	}
}

// A write that moved nothing is not a write.
//
// The guard is n > 0. Every neighbouring bound reads the same on a two-byte
// write, so the boundary needs the two cases that separate them: zero bytes,
// where an entry must not appear, and one byte, where it must.
func TestAWriteAtOfNoBytesRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := f.(writerAt)
	if !ok {
		t.Fatal("the recorder does not offer WriteAt over an os.File")
	}
	n, err := w.WriteAt([]byte{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("the base moved %d bytes for an empty buffer, want 0", n)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	for _, e := range crash.Entries(rec) {
		if e.Kind == "write" {
			t.Errorf("a WriteAt that moved no bytes was recorded at offset %d", e.Off)
		}
	}
}

func TestAWriteAtOfOneByteIsRecorded(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := f.(writerAt)
	if !ok {
		t.Fatal("the recorder does not offer WriteAt over an os.File")
	}
	if _, err := w.WriteAt([]byte("X"), 3); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var writes []crash.Entry
	for _, e := range crash.Entries(rec) {
		if e.Kind == "write" {
			writes = append(writes, e)
		}
	}
	if len(writes) != 1 {
		t.Fatalf("got %d write entries for a one-byte WriteAt, want 1", len(writes))
	}
	if writes[0].Off != 3 || !bytes.Equal(writes[0].Data, []byte("X")) {
		t.Errorf("recorded offset %d data %q, want offset 3 data %q", writes[0].Off, writes[0].Data, "X")
	}
}
