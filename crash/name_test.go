package crash

import (
	"strings"
	"testing"
)

// A unit is named structurally, not by ordinal. role.Key exists because an
// ordinal means nothing outside the run that produced it, and a subtest name
// has the same duty plus a harder one: go test -run must match it later.
func TestAUnitIsNamedByFileOperationAndOccurrence(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "data.tmp"},
		{n: 2, k: kWrite, path: "data.tmp"},
		{n: 3, k: kWrite, path: "data.tmp"},
	}
	byIndex := index(entries)

	if got := unitName(byIndex, unit{entry: 3}); got != "data.tmp:write2" {
		t.Errorf("unitName = %q, want %q", got, "data.tmp:write2")
	}
}

// The range [4096,8192) with sect=1 does not discriminate a carried sect
// from a derived one: from/(to-from) is 4096/4096 = 1, the same answer. The
// short final sector of a 10000-byte write at sector 4096 does discriminate:
// from=8192, to=10000, and 8192/(10000-8192) = 8192/1808 = 4, not 2. See
// units_test.go's TestSectIsCarriedNotDerivedFromTheRange, which guards the
// same case on the side that PRODUCES sect; this guards the side that
// CONSUMES it and reaches the subtest name.
func TestASectorUnitCarriesItsIndex(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 10000)}}
	byIndex := index(entries)

	got := unitName(byIndex, unit{entry: 1, from: 8192, to: 10000, sect: 2})
	if !strings.HasSuffix(got, ".s2") {
		t.Errorf("unitName = %q, want a .s2 suffix -- sect must be read from the unit, not derived from from/(to-from)", got)
	}
}

// A slash would create a third level of subtest nesting and split one state's
// name across two levels.
func TestAStateNameCarriesNoSlash(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "sub/dir/a"}}
	byIndex := index(entries)

	if got := stateName(byIndex, []unit{{entry: 1}}); strings.Contains(got, "/") {
		t.Errorf("stateName = %q, which contains a slash and would nest", got)
	}
}

// Go's own testing.rewrite (src/testing/match.go) replaces every rune in its
// isSpace set with an underscore when it builds the subtest name -run
// actually matches -- not only the ASCII space. A tab left untouched here
// would render one way in this package's own name and a different way in
// the name go test uses, and the two would stop matching.
func TestSafeReplacesATabLikeGosOwnRewriteDoes(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a\tb"}}
	byIndex := index(entries)

	got := unitName(byIndex, unit{entry: 1})
	if strings.Contains(got, "\t") {
		t.Errorf("unitName = %q, still carries a literal tab", got)
	}
	if want := "a_b:write1"; got != want {
		t.Errorf("unitName = %q, want %q", got, want)
	}
}

func TestAnEmptyLostSetIsNamedNone(t *testing.T) {
	if got := stateName(map[int]entry{}, nil); got != "lost=none" {
		t.Errorf("stateName = %q, want %q", got, "lost=none")
	}
}

// A long name becomes a count and a hash, so it stays matchable by -run. The
// detail belongs in the failure message either way.
func TestALongNameBecomesACountAndAHash(t *testing.T) {
	var entries []entry
	var lost []unit
	for i := 1; i <= 30; i++ {
		entries = append(entries, entry{n: i, k: kWrite, path: "averylongfilename"})
		lost = append(lost, unit{entry: i})
	}
	got := stateName(index(entries), lost)

	if len(got) > maxNameLen {
		t.Errorf("stateName is %d characters, want at most %d", len(got), maxNameLen)
	}
	if !strings.Contains(got, "30units:") {
		t.Errorf("stateName = %q, want it to carry the count", got)
	}
}

// The same lost set must produce the same name on every run, or -run cannot
// re-run a failure.
func TestAStateNameIsStable(t *testing.T) {
	entries := []entry{
		{n: 1, k: kWrite, path: "a"},
		{n: 2, k: kWrite, path: "b"},
	}
	byIndex := index(entries)
	lost := []unit{{entry: 2}, {entry: 1}}

	first := stateName(byIndex, lost)
	for i := 0; i < 50; i++ {
		if got := stateName(byIndex, lost); got != first {
			t.Fatalf("stateName changed between runs: %q then %q", first, got)
		}
	}
}

// Two different lost sets of the SAME size must not collapse to the same
// hashed name. The count prefix alone cannot tell them apart when the counts
// match, so this exercises the hash itself, not just the label in front of
// it.
func TestTwoLongLostSetsWithTheSameCountHashDifferently(t *testing.T) {
	var entries []entry
	for i := 1; i <= 31; i++ {
		entries = append(entries, entry{n: i, k: kWrite, path: "averylongfilename"})
	}
	byIndex := index(entries)

	var lostA, lostB []unit
	for i := 1; i <= 30; i++ {
		lostA = append(lostA, unit{entry: i})
	}
	for i := 2; i <= 31; i++ {
		lostB = append(lostB, unit{entry: i})
	}

	nameA := stateName(byIndex, lostA)
	nameB := stateName(byIndex, lostB)
	if !strings.Contains(nameA, "30units:") || !strings.Contains(nameB, "30units:") {
		t.Fatalf("expected both names to carry the same count prefix, got %q and %q", nameA, nameB)
	}
	if nameA == nameB {
		t.Errorf("two different 30-unit lost sets both named %q", nameA)
	}
}

// occurrences numbers each entry within its own (path, kind) sequence. A read
// and an open on the same path are DIFFERENT operations, so each must keep
// its own count -- merging them under one counter would make "the second
// open" mean "the second open-or-read", which is not what the name claims.
func TestOccurrencesCountsEachKindSeparately(t *testing.T) {
	entries := []entry{
		{n: 1, k: kOpen, path: "a"},
		{n: 2, k: kRead, path: "a"},
		{n: 3, k: kOpen, path: "a"},
	}
	got := occurrences(entries)
	if got[1] != 1 {
		t.Errorf("occurrences()[1] = %d, want 1 -- the first open on a", got[1])
	}
	if got[2] != 1 {
		t.Errorf("occurrences()[2] = %d, want 1 -- the first read on a, a different operation from the open before it", got[2])
	}
	if got[3] != 2 {
		t.Errorf("occurrences()[3] = %d, want 2 -- the second open on a", got[3])
	}
}

// unitName rebuilds its occurrence numbering from byIndex, a map, on every
// call. A two-entry case can pass by luck even when the numbering is never
// sorted, because there is only one order to get wrong. Twenty entries on one
// path, checked across fifty calls, gives Go's randomised map iteration a
// real chance to expose a missing sort.
func TestOccurrenceNumberingIsStableAcrossMapIteration(t *testing.T) {
	var entries []entry
	for i := 1; i <= 20; i++ {
		entries = append(entries, entry{n: i, k: kWrite, path: "data.tmp"})
	}
	byIndex := index(entries)

	want := "data.tmp:write13"
	for i := 0; i < 50; i++ {
		if got := unitName(byIndex, unit{entry: 13}); got != want {
			t.Fatalf("unitName = %q on call %d, want %q -- occurrence numbering must follow entry order, not map iteration order", got, i, want)
		}
	}
}

// pointName names the crash point itself: the operation the process died
// after.
func TestAPointIsNamedAfterTheEntryTheProcessDiedAfter(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "data.tmp"},
		{n: 2, k: kWrite, path: "data.tmp"},
	}
	byIndex := index(entries)

	if got := pointName(byIndex, 2); got != "after=data.tmp:write1" {
		t.Errorf("pointName = %q, want %q", got, "after=data.tmp:write1")
	}
}

// opNames is now the ONE table that turns a kind into a word: the record's
// state names and the test-visible Entry.Kind both read it. A kind missing from
// it renders an empty string rather than failing, so a new kind would silently
// merge its occurrence counts with another kind's.
//
// The loop runs from the first kind to the last, and the length check catches a
// kind added AFTER kSync, which the loop alone would not reach.
func TestEveryKindHasExactlyOneName(t *testing.T) {
	for k := kRead; k <= kSync; k++ {
		if opNames[k] == "" {
			t.Errorf("kind %d has no name in opNames", int(k))
		}
	}
	if len(opNames) != int(kSync)+1 {
		t.Errorf("opNames holds %d names for %d kinds", len(opNames), int(kSync)+1)
	}
}
