package crash

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// subsets walks mask 0 through 7 in binary order and marks unit i LOST when
// bit i of mask is set, so the eight subsets come back in this order: {},
// {1}, {2}, {1,2}, {3}, {1,3}, {2,3}, {1,2,3}. Worked by hand from
// state.go's Exhaustive branch, not read back from a run.
//
// The order matters, because the per-point dedup in run.go keeps the first
// lost set that rebuilds a given tree, so this order decides which name a
// state gets.
func TestExhaustiveVisitsEverySubset(t *testing.T) {
	u := []unit{{entry: 1}, {entry: 2}, {entry: 3}}
	got, err := subsets(u, Exhaustive)
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}
	// entriesOf renders each lost set by the entries it holds, not by
	// reflect.DeepEqual, which would pin a nil first element: a correct
	// subsets that returned an empty non-nil slice for mask 0 must still
	// pass.
	want := []string{
		"",
		"1",
		"2",
		"1,2",
		"3",
		"1,3",
		"2,3",
		"1,2,3",
	}
	gotEntries := make([]string, len(got))
	for i, s := range got {
		gotEntries[i] = entriesOf(s)
	}
	if !slices.Equal(gotEntries, want) {
		t.Errorf("subsets =\n  %v\nwant\n  %v", gotEntries, want)
	}
}

// entriesOf renders a lost set as its entry numbers, comma-separated, so a
// test can compare sets by the entries they hold, for example "1,2", and ""
// for an empty set.
func entriesOf(u []unit) string {
	parts := make([]string, len(u))
	for i, x := range u {
		parts[i] = strconv.Itoa(x.entry)
	}
	return strings.Join(parts, ",")
}

func TestPrefixesVisitsOnePerBoundary(t *testing.T) {
	u := []unit{{entry: 1}, {entry: 2}, {entry: 3}}
	got, err := subsets(u, Prefixes)
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("got %d subsets, want 4 = u+1", len(got))
	}
}

// The pending units persist in log order, so what is LOST is a SUFFIX of
// that order: the earliest unit survives longest, and a single-unit loss
// must be the last unit. Swapping suffix for prefix keeps the right COUNT
// (u+1 either way) but gives the wrong units, and a count-only test cannot
// see that. This test names the units, not just their number.
func TestPrefixesLoseASuffixNotAPrefix(t *testing.T) {
	u := []unit{{entry: 1}, {entry: 2}, {entry: 3}}
	got, err := subsets(u, Prefixes)
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}

	for _, s := range got {
		switch len(s) {
		case 1:
			if s[0].entry != 3 {
				t.Errorf("single-unit loss is entry %d, want entry 3 (the last unit)", s[0].entry)
			}
		case 2:
			has := map[int]bool{}
			for _, x := range s {
				has[x.entry] = true
			}
			if !has[2] || !has[3] {
				t.Errorf("two-unit loss is %v, want entries {2,3}", s)
			}
		}
	}
}

// Prefixes must be a subset of Exhaustive for the same units, or the two are
// not two covers of one space.
func TestPrefixesAreASubsetOfExhaustive(t *testing.T) {
	u := []unit{{entry: 1}, {entry: 2}, {entry: 3}}
	all, _ := subsets(u, Exhaustive)
	pre, _ := subsets(u, Prefixes)

	seen := map[string]bool{}
	for _, s := range all {
		seen[keyOf(s)] = true
	}
	for _, s := range pre {
		if !seen[keyOf(s)] {
			t.Errorf("prefix state %v is not among the exhaustive states", s)
		}
	}
}

// Passing the cap fails, and it must not quietly visit a subset: a partial
// walk reports a pass the same way a complete one does.
func TestExhaustiveRefusesPastTheCap(t *testing.T) {
	u := make([]unit, 40) // 2^40 states
	for i := range u {
		u[i] = unit{entry: i + 1}
	}
	_, err := subsets(u, Exhaustive)
	if !errors.Is(err, errTooManyStates) {
		t.Fatalf("err = %v, want errTooManyStates", err)
	}
}

// The cap sits at exactly maxStates, and the boundary must not be off by
// one in either direction: the unit count that lands exactly on the cap is
// accepted, and one more unit is refused.
func TestExhaustiveCapIsInclusiveAtTheBoundary(t *testing.T) {
	n := 0
	for total := 1; total < maxStates; total *= 2 {
		n++
	}
	u := make([]unit, n)
	for i := range u {
		u[i] = unit{entry: i + 1}
	}
	got, err := subsets(u, Exhaustive)
	if err != nil {
		t.Fatalf("subsets at the cap: %v", err)
	}
	if len(got) != maxStates {
		t.Fatalf("got %d states, want %d", len(got), maxStates)
	}

	u = append(u, unit{entry: n + 1})
	if _, err := subsets(u, Exhaustive); !errors.Is(err, errTooManyStates) {
		t.Fatalf("one unit past the cap: err = %v, want errTooManyStates", err)
	}
}

// The diagnostic must render 2^n without overflowing for a large n, so the
// message states the real size rather than a wrapped one. n = 100 is well
// past where an int64 shift would go negative.
func TestExhaustiveCapMessageDoesNotOverflowForLargeN(t *testing.T) {
	u := make([]unit, 100)
	for i := range u {
		u[i] = unit{entry: i + 1}
	}
	_, err := subsets(u, Exhaustive)
	if !errors.Is(err, errTooManyStates) {
		t.Fatalf("err = %v, want errTooManyStates", err)
	}
	if !strings.Contains(err.Error(), "2^100") {
		t.Errorf("err = %q, want it to name 2^100 rather than a wrapped number", err)
	}
	if strings.Contains(err.Error(), "-") {
		t.Errorf("err = %q, contains a minus sign, which means the count wrapped negative", err)
	}
}

// keyOf builds a per-unit key from entry, from, to, and sect. entry alone is
// not enough: sectorsOf splits one write into several units that all share
// one entry number and differ only in from/to/sect, so a key built from
// entry alone would treat those distinct units as identical and let a false
// match through TestPrefixesAreASubsetOfExhaustive.
func keyOf(us []unit) string {
	s := ""
	for _, u := range us {
		s += fmt.Sprintf("%d.%d.%d.%d;", u.entry, u.from, u.to, u.sect)
	}
	return s
}
