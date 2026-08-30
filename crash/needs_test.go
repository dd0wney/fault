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

// O_CREATE on a path already on disk before Record started must not be
// recorded as a create, and a write into it must depend on nothing. A
// flag-only guess (O_CREATE means create) gets this wrong, because the OS
// itself treats O_CREATE on an existing path as a plain open. A spurious
// create entry would give the write a dependency on an entry no crash can
// drop, so states that should exist would stop being generated. This is the
// regression guard for that fix.
func TestOCreateOnAFileThatAlreadyExistsRecordsNoCreate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, e := range crash.Entries(rec) {
		if e.Kind == "create" {
			t.Fatalf("recorded a create entry for a path that predates Record: %+v", e)
		}
	}

	if _, err := f.Write([]byte("v2")); err != nil {
		t.Fatalf("write: %v", err)
	}

	needs := crash.Needs(rec)
	if len(needs) < 2 {
		t.Fatalf("got %d entries, want at least 2", len(needs))
	}
	if len(needs[1]) != 0 {
		t.Errorf("the write needs %v, want none — O_CREATE on an existing file is not a create", needs[1])
	}
}

// A path that predates Record has no origin entry, so a naive "existed
// before" answer that only ever consults the initial snapshot never learns
// that this run removed it. Recreating it with O_CREATE must still record a
// create, and a write after that must depend on it, or a replay later hits a
// write with no entry that created its file.
func TestRemoveThenRecreateOfASnapshotPathRecordsACreate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)

	if err := rec.Remove(filepath.Join(dir, "a")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	entries := crash.Entries(rec)
	createIdx := -1
	for i, e := range entries {
		if e.Kind == "create" {
			createIdx = i
		}
	}
	if createIdx == -1 {
		t.Fatalf("recorded no create entry for a path removed then recreated: %+v", entries)
	}

	if _, err := f.Write([]byte("v2")); err != nil {
		t.Fatalf("write: %v", err)
	}

	needs := crash.Needs(rec)
	write := needs[len(needs)-1]
	wantN := entries[createIdx].N
	if len(write) != 1 || write[0] != wantN {
		t.Errorf("the write needs %v, want [%d] — the recreate", write, wantN)
	}
}
