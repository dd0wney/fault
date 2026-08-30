package crash_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// Every call gets an index, including one that changes nothing. The index is
// what keeps a crash point's number equal to the number fault.Sweep would give
// the same operation, which is the whole of what this package owes a later
// version that walks crash points through an injected error run.
func TestEveryCallGetsAnIndexIncludingAReadOne(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	name := filepath.Join(dir, "a")
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.Read(make([]byte, 1)); err != nil && err.Error() != "EOF" {
		// a read past the end is fine; the point is that it is counted
		_ = err
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := crash.Entries(rec)
	if len(got) < 4 {
		t.Fatalf("got %d entries, want at least 4 (open, write, read, close)", len(got))
	}
	for i, e := range got {
		if e.N != i+1 {
			t.Errorf("entry %d has N = %d, want %d — indexes must be 1-based and gapless", i, e.N, i+1)
		}
	}
}

// Wrap, do not replace. Every call the recorder does not need to change is
// served for real, so the scenario writes real bytes to a real disk.
func TestTheRecorderServesEveryCallForReal(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	name := filepath.Join(dir, "a")
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Read it with the os package, bypassing the recorder entirely.
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("the recorder did not write real bytes: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("on disk = %q, want %q", got, "hello")
	}
}

// A name outside root cannot be rebuilt. Leaving it out of the record in
// silence would build states that no crash can produce, and the sweep would
// still pass. The refusal is held and reported, not panicked.
func TestANameOutsideRootIsRefused(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere")
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(outside, os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		_ = f.Close()
	}

	if crash.Failure(rec) == nil {
		t.Fatal("a write outside root left no refusal, so Run would report a pass")
	}
}
