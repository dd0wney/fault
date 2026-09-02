package crash

import (
	"reflect"
	"strings"
	"testing"
)

func TestSectorZeroLosesAWholeWriteCall(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", data: make([]byte, 10000)}}
	got := units(entries, []int{1}, 1, Model{Sector: 0})
	if len(got) != 1 {
		t.Fatalf("got %d units, want 1 for a whole call", len(got))
	}
	if !got[0].whole() {
		t.Errorf("unit = %+v, want a whole unit — Sector 0 never splits", got[0])
	}
	if got[0].entry != 1 {
		t.Errorf("unit entry = %d, want 1", got[0].entry)
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

// With SplitAtCrashOnly OFF, every pending write that crosses a boundary
// splits, not only the one at the crash point. The test above pins the flag
// on; nothing pinned it off, so the guard could ignore the flag and force
// every earlier write whole while the whole suite stayed green. The ./crash/
// baseline row recorded this gap by name.
//
// MEASURED 2026-09-02: with the guard changed to `splittable && true && n != k`,
// every test in this package passed.
func TestEveryPendingWriteSplitsWhenSplitAtCrashOnlyIsOff(t *testing.T) {
	entries := []entry{
		{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 10000)},
		{n: 2, k: kWrite, path: "a", off: 10000, data: make([]byte, 10000)},
	}
	got := units(entries, []int{1, 2}, 2, Model{Sector: 4096})

	var forEntry1, forEntry2 int
	for _, u := range got {
		switch u.entry {
		case 1:
			forEntry1++
		case 2:
			forEntry2++
		}
	}
	if forEntry1 != 3 {
		t.Errorf("entry 1, before the crash point, produced %d units, want 3 -- the flag is off, so it splits too", forEntry1)
	}
	if forEntry2 != 3 {
		t.Errorf("entry 2, at the crash point, produced %d units, want 3", forEntry2)
	}
}

// A write that never crosses a sector boundary was not split, so its unit must
// be the whole one. Naming it ".s0" claims a granularity that did not apply,
// and it gives Model{Sector: 0} and a sector larger than every write two
// different names for one state. A state name is a go test -run address, so
// two addresses for one state is a defect, not a cosmetic difference.
func TestAWriteInsideOneSectorIsNotSplit(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 100)}}
	got := units(entries, []int{1}, 1, Model{Sector: 4096})
	if len(got) != 1 {
		t.Fatalf("got %d units, want 1", len(got))
	}
	if !got[0].whole() {
		t.Errorf("unit = %+v, want a whole unit — the write fits inside one sector", got[0])
	}
	if name := unitName(index(entries), got[0]); strings.Contains(name, ".s") {
		t.Errorf("unitName = %q, want no sector suffix — the write was never split", name)
	}
}

// A write that exactly fills its sector is the boundary case of the same rule.
// It ends on a boundary without crossing one, so it is still one unit.
func TestAWriteThatExactlyFillsOneSectorIsNotSplit(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 4096)}}
	got := units(entries, []int{1}, 1, Model{Sector: 4096})
	if len(got) != 1 {
		t.Fatalf("got %d units, want 1", len(got))
	}
	if !got[0].whole() {
		t.Errorf("unit = %+v, want a whole unit", got[0])
	}
}

// Collapsing the single-unit case must not collapse a write that genuinely
// crosses a boundary. 100 bytes at offset 4000 with sector 4096 lies partly in
// sector 0 and partly in sector 1, and both units keep their suffixes.
func TestAWriteThatStraddlesABoundaryKeepsBothSectors(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", off: 4000, data: make([]byte, 100)}}
	got := units(entries, []int{1}, 1, Model{Sector: 4096})
	if len(got) != 2 {
		t.Fatalf("got %d units, want 2 — the write crosses a sector boundary", len(got))
	}
	if got[0].from != 0 || got[0].to != 96 || got[0].sect != 0 {
		t.Errorf("unit 0 = %+v, want from 0, to 96, sect 0", got[0])
	}
	if got[1].from != 96 || got[1].to != 100 || got[1].sect != 1 {
		t.Errorf("unit 1 = %+v, want from 96, to 100, sect 1", got[1])
	}

	byIndex := index(entries)
	for i, want := range []string{".s0", ".s1"} {
		if name := unitName(byIndex, got[i]); !strings.HasSuffix(name, want) {
			t.Errorf("unitName(unit %d) = %q, want a %s suffix", i, name, want)
		}
	}
}

// The property the collapse exists for. A sector larger than every write can
// split nothing, so it must give the same units as a whole-call model -- the
// same ranges, the same sector numbers, and therefore the same names.
func TestAHugeSectorGivesTheSameUnitsAsAWholeCallModel(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", off: 0, data: make([]byte, 100)},
	}
	whole := units(entries, []int{1, 2}, 2, Model{Sector: 0})
	huge := units(entries, []int{1, 2}, 2, Model{Sector: 1 << 20})
	if !reflect.DeepEqual(whole, huge) {
		t.Errorf("Sector 0 gave %+v, a 1 MiB sector gave %+v", whole, huge)
	}
}
