package crash_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// The walk must continue past a duplicate state to the distinct states after
// it. Within one crash point, two lost sets that rebuild the same tree are one
// state, and plan skips the second. Nothing pinned that the skip is a skip and
// not a stop: with that `continue` turned into `break`, every state after the
// first duplicate at a crash point is dropped in silence, and fewer subtests
// all pass.
//
// The record is the smallest one with a duplicate FOLLOWED by a distinct tree.
// Two handles each write "X" at offset 0 of the same file. At the second
// write's crash point the pending units are the create, write 1 and write 2,
// and subsets enumerates them in bitmask order:
//
//	none              "X"     new
//	create            absent  new
//	write1            "X"     duplicate -- a break stops here
//	create+write1     absent  duplicate
//	write2            "X"     duplicate
//	create+write2     absent  duplicate
//	write1+write2     ""      NEW, and only a continue reaches it
//	all three         absent  duplicate
//
// The names are worked by hand from the record, not read back from a run, for
// the reason TestAnInPlaceWriteProducesEveryTornState gives. MEASURED
// 2026-09-02: with continue turned into break, every test in this package
// passed.
func TestTheWalkContinuesPastADuplicateState(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)
	name := filepath.Join(dir, "a")

	// Two handles, so the second write lands at offset 0 as the first did. A
	// second write on one handle would land at offset 1 and rebuild a
	// different tree, which is not the duplicate this test needs.
	for range 2 {
		f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("X")); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	got, err := crash.Plan(rec, crash.Model{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := []string{
		"after=a:create1/lost=a:create1",
		"after=a:create1/lost=none",
		"after=a:write1/lost=a:create1",
		"after=a:write1/lost=a:write1",
		"after=a:write1/lost=none",
		"after=a:write2/lost=a:create1",
		"after=a:write2/lost=a:write1+a:write2",
		"after=a:write2/lost=none",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan gave\n  %q\nwant\n  %q", got, want)
	}
}
