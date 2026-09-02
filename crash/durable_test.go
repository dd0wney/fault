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

// A sync that has not run yet covers nothing. The crash point bounds what
// happened: the sync at 3 is after the crash at 2, so the write at 2 is still
// pending when the power goes.
//
// Every other test in this file puts the crash point at the LAST entry, so
// none of them has an entry past k, and a split that read past k survived all
// of them. MEASURED 2026-09-02: deleting the `if e.n > k { continue }` guard
// passed the whole crash suite. With it gone, a sync recorded after the crash
// point marks the file synced, every earlier write to it reads durable, and
// the walk never builds the state that loses the write before its sync --
// which is the central case of crash simulation.
func TestASyncAfterTheCrashPointCoversNothing(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a"},
		{n: 3, k: kSync, path: "a"},
	}
	durable, pending := split(entries, 2, Model{})
	if !contains(pending, 2) {
		t.Errorf("write 2 is %v/%v, want pending — the sync at 3 had not run when the power went", durable, pending)
	}
}

// A create in a nested directory is covered by a sync on THAT directory, and
// needs no sync on the root. The rule's second term, dirSynced[path.Dir(e.to)],
// applies only to a rename; for every other metadata entry e.to is empty, and
// the `e.to == ""` short-circuit is what keeps path.Dir("") -- which is "." --
// out of the decision. Remove it and a create under "d" also needs the root
// synced. Every reference store puts its files at the root, where "." is the
// directory anyway, so nothing could see the difference. The ./crash/ baseline
// row recorded this gap by name.
//
// MEASURED 2026-09-02: with the term changed to `(false || dirSynced[...])`,
// every test in this package passed.
func TestADirectorySyncCoversACreateInThatDirectoryAlone(t *testing.T) {
	entries := []entry{
		{n: 1, k: kMkdir, path: "d"},
		{n: 2, k: kCreate, path: "d/a"},
		{n: 3, k: kSync, path: "d", dir: true},
	}
	durable, pending := split(entries, 3, Model{})
	if !contains(durable, 2) {
		t.Errorf("the create in d is %v/%v, want durable -- its own directory was synced, and the root need not be", durable, pending)
	}
}

// split partitions the MUTATING entries and no other. A read, an open or a
// sync in either set would become a loss unit, and a lost unit that changes
// no bytes rebuilds the same tree as losing nothing, so dedup hides it from
// every plan-level test. The unit level is the only place that can see it.
//
// MEASURED 2026-09-02: with the `!e.k.mutates()` skip removed, every test in
// this package passed.
func TestSplitPartitionsOnlyTheMutatingEntries(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kRead, path: "a"},
		{n: 3, k: kSync, path: "a"},
		{n: 4, k: kWrite, path: "a"},
	}
	durable, pending := split(entries, 4, Model{})
	for _, n := range []int{2, 3} {
		if contains(durable, n) || contains(pending, n) {
			t.Errorf("entry %d does not mutate and split placed it in %v/%v", n, durable, pending)
		}
	}
	// Both mutating entries are pending: the file sync at 3 covers no write
	// before it, and no directory sync covers the create. The first version
	// of this assertion expected the create durable, and the green run said
	// no -- the sync is on the file, not the directory.
	if !contains(pending, 1) || !contains(pending, 4) || len(durable) != 0 {
		t.Errorf("the mutating entries are %v/%v, want both pending", durable, pending)
	}
}

// The same rule for metadata: a directory sync after the crash point does not
// make the rename durable.
func TestADirectorySyncAfterTheCrashPointCoversNothing(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kRename, path: "tmp", to: "data"},
		{n: 3, k: kSync, path: ".", dir: true},
	}
	durable, pending := split(entries, 2, Model{})
	if !contains(pending, 2) {
		t.Errorf("the rename is %v/%v, want pending — the directory sync at 3 had not run", durable, pending)
	}
}
