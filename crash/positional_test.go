package crash_test

import (
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
