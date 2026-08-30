package crash_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// A write needs the create of the file it writes into. Without this, a state
// could hold data for a file that has no directory entry, which no power cut
// can produce. Findings against such a state are noise, and noise trains a
// reader to ignore the tool.
func TestAWriteNeedsTheCreateOfItsFile(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}

	needs := crash.Needs(rec)
	// entry 1 is the create, entry 2 is the write
	if len(needs) < 2 {
		t.Fatalf("got %d entries, want at least 2", len(needs))
	}
	if len(needs[1]) != 1 || needs[1][0] != 1 {
		t.Errorf("the write needs %v, want [1] — the index of the create", needs[1])
	}
}

// A rename needs the create of the name it moves, and it becomes the origin of
// the new name. A write after the rename therefore needs the rename.
func TestARenameCarriesTheOriginToTheNewName(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "tmp"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = f.Close()
	if err := rec.Rename(filepath.Join(dir, "tmp"), filepath.Join(dir, "data")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	needs := crash.Needs(rec)
	// 1 create tmp, 2 close, 3 rename
	if len(needs) < 3 {
		t.Fatalf("got %d entries, want at least 3", len(needs))
	}
	if len(needs[2]) != 1 || needs[2][0] != 1 {
		t.Errorf("the rename needs %v, want [1] — the create of tmp", needs[2])
	}
}

// A file that existed before Record has no create entry, so nothing depends on
// one. It arrives from the snapshot and is durable by construction. This is
// what lets a test build its initial state before recording.
func TestAPreExistingFileHasNoDependency(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("v2")); err != nil {
		t.Fatalf("write: %v", err)
	}

	needs := crash.Needs(rec)
	if len(needs) < 2 {
		t.Fatalf("got %d entries, want at least 2", len(needs))
	}
	if len(needs[1]) != 0 {
		t.Errorf("the write needs %v, want none — the file predates the record", needs[1])
	}
}
