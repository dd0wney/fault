package crash_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// The offset accumulates across sequential writes on one handle: the third
// write lands where the first two ended, not where the second did.
//
// The ./crash/ baseline row recorded this gap by name: "no scenario writes
// twice on one handle, so f.off += reads the same as f.off =". Two writes
// are not enough to see it, because after the second write both forms hold
// the second write's length when the first write's length equals it. Three
// writes of two bytes give offsets 0, 2, 4 under addition and 0, 2, 2 under
// assignment, and the replay control would then rebuild "ABEF" against a
// disk that holds "ABCDEF".
//
// MEASURED 2026-09-02: with `f.off += int64(n)` changed to `f.off = int64(n)`
// in Write, every test in this package passed.
func TestSequentialWritesAccumulateTheOffset(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"AB", "CD", "EF"} {
		if _, err := f.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if got, want := writeOffsets(rec), []int64{0, 2, 4}; !reflect.DeepEqual(got, want) {
		t.Errorf("three sequential writes are recorded at %v, want %v", got, want)
	}
}

// The offset accumulates across sequential reads too, so a write after two
// reads lands where the reads ended. The same mutant survives in Read for the
// same reason: no scenario read twice on one handle before it wrote.
func TestSequentialReadsAccumulateTheOffset(t *testing.T) {
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
	for range 2 {
		if _, err := f.Read(make([]byte, 2)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Write([]byte("XY")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if got, want := writeOffsets(rec), []int64{4}; !reflect.DeepEqual(got, want) {
		t.Errorf("the write after two reads is recorded at %v, want %v", got, want)
	}
}

// writeOffsets returns the recorded offset of every write, in record order.
func writeOffsets(rec *crash.Recorder) []int64 {
	var out []int64
	for _, e := range crash.Entries(rec) {
		if e.Kind == "write" {
			out = append(out, e.Off)
		}
	}
	return out
}
