package crash

import (
	"testing"
)

func TestReplayAppliesEachKindOfEntry(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kWrite, path: "tmp", off: 0, data: []byte("hello")},
		{n: 3, k: kRename, path: "tmp", to: "data"},
		{n: 4, k: kMkdir, path: "d"},
		{n: 5, k: kTruncate, path: "data", size: 2},
	}
	all := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}

	got, err := replay(tree{}, entries, all, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	want := tree{
		"data": {data: []byte("he")},
		"d":    {dir: true},
	}
	if d := diffTree(want, got); d != "" {
		t.Errorf("replay produced the wrong tree:\n%s", d)
	}
}

// A write past the end of a file zero-fills the gap, which is what a real
// filesystem does. Getting this wrong makes the positive control fail on any
// scenario that seeks, and it would be blamed on the scenario.
func TestReplayZeroFillsAGap(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", off: 3, data: []byte("z")},
	}
	got, err := replay(tree{}, entries, map[int]bool{1: true, 2: true}, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := tree{"a": {data: []byte{0, 0, 0, 'z'}}}
	if d := diffTree(want, got); d != "" {
		t.Errorf("a gap was not zero-filled:\n%s", d)
	}
}

// An entry that is absent did not happen, and the replay must not apply it.
func TestReplaySkipsAnAbsentEntry(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", off: 0, data: []byte("hello")},
	}
	got, err := replay(tree{}, entries, map[int]bool{1: true}, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := tree{"a": {data: nil}}
	if d := diffTree(want, got); d != "" {
		t.Errorf("an absent write was applied:\n%s", d)
	}
}

// A lost byte range is NOT zeroed. It keeps whatever the replay already had
// there, because the prior content is already in the record, as an earlier
// entry. This is the graphdb counter-example: an SSTable header is written
// once as a placeholder and backpatched later by a seek to 0. A zeroed
// header reads as IndexOffset = 0, which is structurally valid, so the
// reader parses the body as an index and fails in a way it never fails in
// reality. Manufacturing that failure is worse than reporting nothing.
func TestReplayKeepsALostRangeAtItsPriorContent(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "sst"},
		{n: 2, k: kWrite, path: "sst", off: 0, data: []byte{1, 2, 3, 4}},
		{n: 3, k: kWrite, path: "sst", off: 0, data: []byte{9, 9, 9, 9}},
	}
	present := map[int]bool{1: true, 2: true, 3: true}
	// entry 3, the backpatch, is present but every byte of it was lost.
	lost := map[int][]unit{3: {{from: 0, to: 4}}}

	got, err := replay(tree{}, entries, present, lost)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := tree{"sst": {data: []byte{1, 2, 3, 4}}}
	if d := diffTree(want, got); d != "" {
		t.Errorf("a lost range was not preserved:\n%s", d)
	}
}

// Only the byte ranges that were not lost are applied. A partial loss must
// still let the surviving bytes of the same write land, so a bug that skips
// the whole write when any part of it is lost, and a bug that zeroes the
// lost part, both fail this test.
func TestReplayAppliesOnlyThePresentBytesOfAPartialLoss(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", off: 0, data: []byte("AAAA")},
		{n: 3, k: kWrite, path: "a", off: 0, data: []byte("BBBB")},
	}
	present := map[int]bool{1: true, 2: true, 3: true}
	// bytes 1 and 2 of entry 3's write did not land; bytes 0 and 3 did.
	lost := map[int][]unit{3: {{from: 1, to: 3}}}

	got, err := replay(tree{}, entries, present, lost)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := tree{"a": {data: []byte("BAAB")}}
	if d := diffTree(want, got); d != "" {
		t.Errorf("a partial loss was applied incorrectly:\n%s", d)
	}
}

// A present write into a path that no present entry created must be
// refused, not silently turned into a new file. The caller applies a
// dependency closure meant to prevent this pairing, so this error firing
// means the generator is broken, and every state built after it is a
// fiction.
func TestReplayRefusesToInventAFile(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", off: 0, data: []byte("hello")},
	}
	// entry 1, the create, is absent -- only its dependent write is present.
	_, err := replay(tree{}, entries, map[int]bool{2: true}, nil)
	if err == nil {
		t.Fatal("replay invented a file for a write with no present create")
	}
}

// A present remove of a name no present entry created must be REFUSED, exactly
// as a write, a truncate and a rename are. Deleting an absent key in silence is
// what let a phantom remove -- one the base itself refused -- reach the walk
// and build a state no power cut can produce. The control replays the whole
// record, so this guard is what would have caught it.
func TestReplayRefusesToRemoveAFileNoPresentEntryCreated(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kRemove, path: "a", needs: []int{1}},
	}
	// entry 1, the create, is absent -- only the remove that names it is present.
	_, err := replay(tree{}, entries, map[int]bool{2: true}, nil)
	if err == nil {
		t.Fatal("replay deleted a name no present entry created, in silence")
	}
}

// The refusal above must not fire on a path that arrived from the snapshot. It
// has no create entry by construction, and removing it is the commonest thing a
// scenario does.
func TestReplayRemovesASnapshotPath(t *testing.T) {
	entries := []entry{{n: 1, k: kRemove, path: "a"}}
	got, err := replay(tree{"a": {data: []byte("v1")}}, entries, map[int]bool{1: true}, nil)
	if err != nil {
		t.Fatalf("replay refused to remove a snapshot path: %v", err)
	}
	if d := diffTree(tree{}, got); d != "" {
		t.Errorf("the remove did not apply:\n%s", d)
	}
}
