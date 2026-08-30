package crash

import "testing"

// Dropping a create must drop every write into it. A file with data and no
// directory entry is a state no power cut can make, and a finding against one
// is noise.
func TestDroppingACreateDropsItsWrites(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", needs: []int{1}},
		{n: 3, k: kWrite, path: "a", needs: []int{1}},
	}
	keep := closure(entries, map[int]bool{2: true, 3: true}) // create dropped

	if keep[2] || keep[3] {
		t.Errorf("keep = %v, want the writes dropped with their create", keep)
	}
}

// The closure is transitive: a rename that needs a dropped create is dropped,
// and a write into the renamed name goes with it.
func TestTheClosureIsTransitive(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kRename, path: "tmp", to: "data", needs: []int{1}},
		{n: 3, k: kWrite, path: "data", needs: []int{2}},
	}
	keep := closure(entries, map[int]bool{2: true, 3: true})
	if keep[2] || keep[3] {
		t.Errorf("keep = %v, want both dropped through the rename", keep)
	}
}

// The transitive test above hands entries to closure in index order, and
// needs always point backward, so a single top-down pass over that slice
// already cascades the whole chain -- it would pass even with the outer
// "loop until nothing changes" removed. This test hands the same four-long
// chain to closure in REVERSE slice order, so one forward pass only reaches
// one level (entry 2, whose need is entry 1, already absent from keep) and
// leaves entries 3 and 4 wrongly kept. Only a real fixed-point loop, which
// revisits entry 3 after entry 2 falls and then entry 4 after entry 3 falls,
// drops all three.
func TestClosureReachesAFixedPointRegardlessOfEntryOrder(t *testing.T) {
	entries := []entry{
		{n: 4, k: kWrite, path: "d", needs: []int{3}},
		{n: 3, k: kWrite, path: "d", needs: []int{2}},
		{n: 2, k: kRename, path: "tmp", to: "d", needs: []int{1}},
		{n: 1, k: kCreate, path: "tmp"},
	}
	keep := closure(entries, map[int]bool{2: true, 3: true, 4: true})
	if keep[2] || keep[3] || keep[4] {
		t.Errorf("keep = %v, want all three dropped through the whole chain, not just the first level", keep)
	}
}

// A tree with only two keys can iterate in the same order by chance --
// there are only two orderings, so an implementation that ranges over the
// map directly, instead of sorting, has a coin-flip chance of passing a
// two-key test anyway. This test uses eight keys, built in two different
// insertion orders, and checks agreement across twenty repeated calls (Go
// does not promise the same range order twice, even within one run of one
// process), which drives the chance of an unsorted implementation slipping
// through to roughly one in eight thousand per run.
func TestFingerprintIsStableAndDiscriminating(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	build := func(order []string) tree {
		out := tree{}
		for _, n := range order {
			out[n] = node{data: []byte(n)}
		}
		return out
	}

	forward := build(names)
	reversed := make([]string, len(names))
	for i, n := range names {
		reversed[len(names)-1-i] = n
	}
	backward := build(reversed)

	want := fingerprint(forward)
	for i := 0; i < 20; i++ {
		if got := fingerprint(forward); got != want {
			t.Fatalf("call %d: fingerprint(forward) = %s, want %s -- unstable across repeated calls on the same tree", i, got, want)
		}
		if got := fingerprint(backward); got != want {
			t.Fatalf("call %d: fingerprint(backward) = %s, want %s -- two equal trees fingerprint differently, so deduplication depends on map order", i, got, want)
		}
	}

	changed := build(names)
	changed["a"] = node{data: []byte("different")}
	if fingerprint(changed) == want {
		t.Error("a changed tree fingerprints the same as the original, so distinct states would be merged")
	}
}

// Two files of equal length but different content must not collide. A
// fingerprint that hashed only the length, and not the bytes, would merge
// them.
func TestFingerprintDistinguishesEqualLengthContent(t *testing.T) {
	a := tree{"x": {data: []byte("1")}}
	b := tree{"x": {data: []byte("2")}}
	if fingerprint(a) == fingerprint(b) {
		t.Error("fingerprint(a) == fingerprint(b), want different -- same length, different content, must not collide")
	}
}

// An empty file and a directory of the same name both carry zero bytes of
// data, so a fingerprint that hashed only the name and the data, and not the
// dir flag, would merge them. A crash cannot turn a file into a directory,
// so treating the two states as the same would hide a real difference.
func TestFingerprintDistinguishesFileFromDirectory(t *testing.T) {
	file := tree{"a": {data: []byte{}}}
	dir := tree{"a": {dir: true}}
	if fingerprint(file) == fingerprint(dir) {
		t.Error("fingerprint(file) == fingerprint(dir), want different -- an empty file is not a directory")
	}
}
