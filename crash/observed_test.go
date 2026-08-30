package crash_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// seedThroughSeam writes one file through the recorder, so every test below
// has a non-empty record and a crash point. Without one, errNoMutations would
// refuse the plan and no test here could tell a silent gap from a loud one.
func seedThroughSeam(t *testing.T, rec *crash.Recorder, dir, name string) {
	t.Helper()
	f, err := rec.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	if _, err := f.Write([]byte("seen")); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", name, err)
	}
}

// Observed names every path the recorder served, including the two names of a
// rename. A rename is the operation most likely to be the ONLY one a store
// performs on its destination name, so a report that named the source alone
// would answer "no" for the file the caller actually cares about.
func TestObservedNamesEveryPathTheRecorderServed(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	seedThroughSeam(t, rec, dir, "data.tmp")
	if err := rec.Rename(filepath.Join(dir, "data.tmp"), filepath.Join(dir, "data")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	got := rec.Observed()
	for _, want := range []string{"data.tmp", "data"} {
		if !slices.Contains(got, want) {
			t.Errorf("Observed() = %q, want it to contain %q", got, want)
		}
	}
	if !slices.IsSorted(got) {
		t.Errorf("Observed() = %q, want a sorted list — a caller that prints it needs a stable order", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Errorf("Observed() = %q, want no duplicate: a path served twice is still one path", got)
			break
		}
	}
}

// THE GAP THIS METHOD EXISTS FOR, and the ordinary shape of it: a component
// that writes outside the recorded root is invisible to the recorder AND to
// the replay control, because the control compares the recorded root alone.
//
// The live instance is graphdb's pkg/search/lsa_persistence.go, where SaveToFile
// calls os.OpenFile and os.Rename directly. No FS sees the LSA snapshot, so no
// recorder of ours can observe it, and the sweep beside it reports a pass.
//
// This test asserts BOTH halves. Observed must stay silent about the path, and
// plan must still succeed — a gap that made the plan fail would not be silent
// and would need no new method.
func TestObservedIsSilentAboutAPathOutsideTheRecordedRoot(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	seedThroughSeam(t, rec, dir, "through.dat")
	if err := os.WriteFile(filepath.Join(other, "around.dat"), []byte("unseen"), 0o600); err != nil {
		t.Fatalf("the bypass write: %v", err)
	}

	if got := rec.Observed(); slices.Contains(got, "around.dat") {
		t.Errorf("Observed() = %q, want it NOT to contain a path the recorder never served", got)
	}
	if _, err := crash.Plan(rec, crash.Model{Sector: 4096}); err != nil {
		t.Fatalf("plan failed, so this bypass is not silent and the test proves nothing about a silent one: %v", err)
	}
}

// The second silent shape: a bypass under the recorded root that leaves no net
// difference in the tree. The replay control compares the finished directory,
// so a file written and removed around the seam is gone by the time it looks.
//
// Neither guard covers this. errNoMutations asks "did anything happen", and the
// record is not empty. checkReplay asks "does the tree match", and it does.
func TestObservedIsSilentAboutABypassThatLeavesNoTraceInTheTree(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	seedThroughSeam(t, rec, dir, "through.dat")

	tmp := filepath.Join(dir, "around.tmp")
	if err := os.WriteFile(tmp, []byte("unseen"), 0o600); err != nil {
		t.Fatalf("the bypass write: %v", err)
	}
	if err := os.Remove(tmp); err != nil {
		t.Fatalf("the bypass remove: %v", err)
	}

	if got := rec.Observed(); slices.Contains(got, "around.tmp") {
		t.Errorf("Observed() = %q, want it NOT to contain a path the recorder never served", got)
	}
	if _, err := crash.Plan(rec, crash.Model{Sector: 4096}); err != nil {
		t.Fatalf("plan failed, so this bypass is not silent: %v", err)
	}
}

// The boundary, recorded so no reader over-trusts Observed. A bypass that adds
// a NAME under the recorded root is already caught, loudly, by the replay
// control. Observed is for the silent shapes above, not for this one.
//
// Measured 2026-08-31. Without this test, a later change that weakened the
// control would move work onto Observed with nothing to report the move.
func TestTheReplayControlAlreadyCatchesABypassThatAddsAName(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	seedThroughSeam(t, rec, dir, "through.dat")
	if err := os.WriteFile(filepath.Join(dir, "around.dat"), []byte("unseen"), 0o600); err != nil {
		t.Fatalf("the bypass write: %v", err)
	}

	if _, err := crash.Plan(rec, crash.Model{Sector: 4096}); err == nil {
		t.Fatal("plan succeeded, so the replay control no longer catches a bypass that adds a name under the recorded root")
	}
}

// A path the recorder refused, because it lies outside the root, is not
// something the recorder served. The refusal already fails the run, so the
// empty path must not reach a caller reading this list.
func TestObservedHoldsNoEmptyPath(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	seedThroughSeam(t, rec, dir, "through.dat")

	outside := filepath.Join(other, "outside.dat")
	f, err := rec.OpenFile(outside, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open outside the root: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if crash.Failure(rec) == nil {
		t.Fatal("the recorder did not refuse a name outside the root, so this test no longer covers what it names")
	}
	if got := rec.Observed(); slices.Contains(got, "") {
		t.Errorf("Observed() = %q, want no empty path", got)
	}
}
