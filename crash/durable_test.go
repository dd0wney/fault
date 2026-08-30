package crash

import (
	"reflect"
	"testing"
)

func TestOnlyAMutationCarriesACrashPoint(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kRead, path: "a"},
		{n: 3, k: kWrite, path: "a"},
		{n: 4, k: kSync, path: "a"},
		{n: 5, k: kOpen, path: "a"},
	}
	got := crashPoints(entries)
	want := []int{1, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("crashPoints = %v, want %v — a read, a sync and a plain open change nothing", got, want)
	}
}

// The slice position and the entry number coincide in every other test in
// this file, so none of them would notice crashPoints returning the loop
// index instead of e.n. This entries slice starts at 5 and skips 6, 7 and 8,
// so a position-based answer ([0, 1]) and a number-based answer ([5, 9])
// cannot be confused with each other.
func TestCrashPointsReturnsEntryNumbersNotSlicePositions(t *testing.T) {
	entries := []entry{
		{n: 5, k: kCreate, path: "a"},
		{n: 9, k: kWrite, path: "a"},
	}
	got := crashPoints(entries)
	want := []int{5, 9}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("crashPoints = %v, want %v — the entry number, not the slice position", got, want)
	}
}

func TestAFileSyncMakesThatFilesWritesDurable(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a"},
		{n: 3, k: kSync, path: "a"},
		{n: 4, k: kWrite, path: "a"},
	}
	durable, pending := split(entries, 4, Model{})
	if !contains(durable, 2) {
		t.Errorf("write 2 is %v/%v, want durable — a sync on its own file followed it", durable, pending)
	}
	if !contains(pending, 4) {
		t.Errorf("write 4 is %v/%v, want pending — no sync followed it", durable, pending)
	}
}

// A sync on a file does NOT make a rename durable. That is the missing-parent-
// fsync defect, and it is the most common real one.
//
// The crash point is 5, the trailing sync itself, not 4, the rename. Splitting
// at 4 would ask what a crash leaves BEFORE that file sync even runs, which
// proves nothing about whether the sync covers the rename. Splitting at 5
// lets the sync run first, so a rename still pending afterwards is the
// defect this test exists to catch.
func TestAFileSyncDoesNotMakeARenameDurable(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kWrite, path: "tmp"},
		{n: 3, k: kSync, path: "tmp"},
		{n: 4, k: kRename, path: "tmp", to: "data"},
		{n: 5, k: kSync, path: "tmp"},
	}
	durable, pending := split(entries, 5, Model{})
	if !contains(pending, 4) {
		t.Errorf("the rename is %v/%v, want pending — only a directory sync makes it durable", durable, pending)
	}
}

// The crash point must sit AT OR AFTER the directory sync for the sync to
// cover the rename: a crash cannot be made durable by a call that has not
// run yet. Entry 3 is the sync, so the crash point under test is 3, not 2 —
// splitting at 2 would ask what a crash leaves BEFORE the sync runs, which is
// exactly the pending case TestAFileSyncDoesNotMakeARenameDurable already
// covers.
func TestADirectorySyncMakesMetadataDurable(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kRename, path: "tmp", to: "data"},
		{n: 3, k: kSync, path: ".", dir: true},
	}
	durable, pending := split(entries, 3, Model{})
	_ = pending
	if !contains(durable, 2) {
		t.Errorf("the rename is not durable after a directory sync: durable=%v", durable)
	}
}

// A rename names two paths. Syncing only the source directory must leave it
// pending, and only once both directories are synced does it become durable.
// A check that reads only path.Dir(e.path), or only path.Dir(e.to), would
// pass the first half of this test by accident — the second half catches
// that, because it requires the second sync to change the answer.
func TestRenameAcrossDirectoriesNeedsBothDirectoriesSynced(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "src/tmp"},
		{n: 2, k: kRename, path: "src/tmp", to: "dst/data"},
		{n: 3, k: kSync, path: "src", dir: true},
	}
	durable, pending := split(entries, 3, Model{})
	if !contains(pending, 2) {
		t.Errorf("the rename is %v/%v, want pending — only the source directory was synced", durable, pending)
	}

	entries = append(entries, entry{n: 4, k: kSync, path: "dst", dir: true})
	durable, _ = split(entries, 4, Model{})
	if !contains(durable, 2) {
		t.Errorf("the rename is not durable once both directories are synced: durable=%v", durable)
	}
}

// MetadataDurable is the Windows switch, and this is the only test that can
// catch it being read and then ignored.
func TestMetadataDurableMakesARenameDurableAtOnce(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kRename, path: "tmp", to: "data"},
	}
	durable, _ := split(entries, 2, Model{MetadataDurable: true})
	if !contains(durable, 2) {
		t.Errorf("with MetadataDurable the rename is not durable: durable=%v", durable)
	}
	strict, _ := split(entries, 2, Model{})
	if contains(strict, 2) {
		t.Errorf("without MetadataDurable the rename is durable, so the field does nothing")
	}
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
