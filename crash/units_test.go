package crash

import "testing"

func TestSectorZeroLosesAWholeWriteCall(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", data: make([]byte, 10000)}}
	got := units(entries, []int{1}, 1, Model{Sector: 0})
	if len(got) != 1 {
		t.Fatalf("got %d units, want 1 for a whole call", len(got))
	}
}

func TestASectorSplitsAWriteAtItsBoundaries(t *testing.T) {
	// One write of 10000 bytes at offset 0, sector 4096, gives 3 units:
	// [0,4096) [4096,8192) [8192,10000). The last unit is short, and sect
	// must still read 2 there -- not from/(to-from), which a short last
	// sector sends to the wrong number. See sect's own check below.
	entries := []entry{{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 10000)}}
	got := units(entries, []int{1}, 1, Model{Sector: 4096})
	if len(got) != 3 {
		t.Fatalf("got %d units, want 3", len(got))
	}
	if got[0].from != 0 || got[0].to != 4096 {
		t.Errorf("unit 0 = [%d,%d), want [0,4096)", got[0].from, got[0].to)
	}
	if got[1].from != 4096 || got[1].to != 8192 {
		t.Errorf("unit 1 = [%d,%d), want [4096,8192)", got[1].from, got[1].to)
	}
	if got[2].from != 8192 || got[2].to != 10000 {
		t.Errorf("unit 2 = [%d,%d), want [8192,10000)", got[2].from, got[2].to)
	}
}

// sect is the loop counter, carried rather than derived. A derivation such as
// from/(to-from) names the short last sector wrongly: sector 4096 on a 10000
// byte write ends with from=8192, to=10000, and 8192/1808 gives 4, not 2.
// Task 11 names a state after sect, so a wrong number there is worse than a
// long one, because go test -run then matches nothing.
func TestSectIsCarriedNotDerivedFromTheRange(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 10000)}}
	got := units(entries, []int{1}, 1, Model{Sector: 4096})
	if len(got) != 3 {
		t.Fatalf("got %d units, want 3", len(got))
	}
	want := []int{0, 1, 2}
	for i, u := range got {
		if u.sect != want[i] {
			t.Errorf("unit %d sect = %d, want %d", i, u.sect, want[i])
		}
	}
}

// A metadata entry never splits, whatever the sector size. No filesystem
// half-renames a file.
func TestMetadataNeverSplits(t *testing.T) {
	entries := []entry{{n: 1, k: kRename, path: "a", to: "b"}}
	got := units(entries, []int{1}, 1, Model{Sector: 512})
	if len(got) != 1 {
		t.Fatalf("got %d units for a rename, want 1", len(got))
	}
	if got[0].from != 0 || got[0].to != 0 {
		t.Errorf("a rename unit carries a byte range [%d,%d), want none", got[0].from, got[0].to)
	}
}

// SplitAtCrashOnly keeps the part-written record without paying for it at
// every earlier write. It must split ONLY the write at the crash point k --
// checking the total alone cannot tell "entry 1 split, entry 2 whole" from
// the intended "entry 1 whole, entry 2 split", because both give 4 units.
func TestSplitAtCrashOnlyOnlySplitsTheWriteAtTheCrash(t *testing.T) {
	entries := []entry{
		{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 10000)},
		{n: 2, k: kWrite, path: "a", off: 10000, data: make([]byte, 10000)},
	}
	got := units(entries, []int{1, 2}, 2, Model{Sector: 4096, SplitAtCrashOnly: true})
	if len(got) != 4 {
		t.Fatalf("got %d units, want 4 — only the write at the crash point splits", len(got))
	}

	var forEntry1, forEntry2 int
	for _, u := range got {
		switch u.entry {
		case 1:
			forEntry1++
		case 2:
			forEntry2++
		}
	}
	if forEntry1 != 1 {
		t.Errorf("entry 1, before the crash point, produced %d units, want 1 whole unit", forEntry1)
	}
	if forEntry2 != 3 {
		t.Errorf("entry 2, at the crash point, produced %d units, want 3 split units", forEntry2)
	}
}
