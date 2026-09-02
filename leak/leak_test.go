package leak

import (
	"fmt"
	"testing"
)

// counterFake is a Counter and nothing else -- Report's "not a Namer"
// branch.
type counterFake struct {
	outstanding int
	maxOut      int
}

func (c counterFake) Outstanding() int    { return c.outstanding }
func (c counterFake) MaxOutstanding() int { return c.maxOut }

// namerFake is a Counter that also names the handles behind its count.
type namerFake struct {
	outstanding int
	maxOut      int
	paths       []string
}

func (n namerFake) Outstanding() int    { return n.outstanding }
func (n namerFake) MaxOutstanding() int { return n.maxOut }
func (n namerFake) OpenPaths() []string { return n.paths }

func TestReportIsSilentWhenEveryCountReturnedToZero(t *testing.T) {
	c := counterFake{outstanding: 0, maxOut: 2}

	got := Report(c)
	if len(got) != 0 {
		t.Errorf("Report(%+v) = %v, want no sentence", c, got)
	}
}

func TestReportNamesTheClassAndTheCount(t *testing.T) {
	c := counterFake{outstanding: 3, maxOut: 3}

	got := Report(c)
	want := fmt.Sprintf("%T still holds 3", c)
	if len(got) != 1 || got[0] != want {
		t.Errorf("Report(%+v) = %v, want [%q]", c, got, want)
	}
}

func TestReportNamesTheHandlesBehindTheCount(t *testing.T) {
	// The count and the number of paths agree: two handles, two names.
	n := namerFake{outstanding: 2, maxOut: 2, paths: []string{"wal.log", "wal.log"}}

	got := Report(n)
	want := fmt.Sprintf("%T still holds 2: wal.log, wal.log", n)
	if len(got) != 1 || got[0] != want {
		t.Errorf("Report(%+v) = %v, want [%q]", n, got, want)
	}
}

// TestReportSortsThePathsRegardlessOfInputOrder pins a mutation gate
// finding: the fixture above holds two IDENTICAL names, so removing the
// sort inside Report changed no test result there. This fixture holds two
// DISTINCT, out-of-order names, which only a real sort puts back in order.
func TestReportSortsThePathsRegardlessOfInputOrder(t *testing.T) {
	n := namerFake{outstanding: 2, maxOut: 2, paths: []string{"zeta.log", "alpha.log"}}

	got := Report(n)
	want := fmt.Sprintf("%T still holds 2: alpha.log, zeta.log", n)
	if len(got) != 1 || got[0] != want {
		t.Errorf("Report(%+v) = %v, want [%q]", n, got, want)
	}
}

func TestReportRefusesACountThatNeverRose(t *testing.T) {
	c := counterFake{outstanding: 0, maxOut: 0}

	got := Report(c)
	want := fmt.Sprintf("%T never held anything, so its leak check proved nothing", c)
	if len(got) != 1 || got[0] != want {
		t.Errorf("Report(%+v) = %v, want [%q]", c, got, want)
	}
}

func TestReportReportsEveryCounterNotJustTheFirst(t *testing.T) {
	first := counterFake{outstanding: 1, maxOut: 1}
	second := counterFake{outstanding: 0, maxOut: 0}

	got := Report(first, second)
	want := []string{
		fmt.Sprintf("%T still holds 1", first),
		fmt.Sprintf("%T never held anything, so its leak check proved nothing", second),
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Report(first, second) = %v, want %v", got, want)
	}
}

// TestReportSaysWhenOutstandingAndOpenPathsDisagree pins the reading a Namer
// gives when the two answers disagree: Outstanding gives the count, OpenPaths
// gives the names, and Report states each rather than picking one over the
// other.
func TestReportSaysWhenOutstandingAndOpenPathsDisagree(t *testing.T) {
	n := namerFake{outstanding: 2, maxOut: 2, paths: []string{"c", "a", "b"}}

	got := Report(n)
	want := fmt.Sprintf("%T still holds 2, and OpenPaths names 3: a, b, c", n)
	if len(got) != 1 || got[0] != want {
		t.Errorf("Report(%+v) = %v, want [%q]", n, got, want)
	}
}

// TestReportGivenANilCounterSaysSoInsteadOfPanicking pins change 4: a nil
// counter used to reach MaxOutstanding on a nil interface and panic. The
// recover below turns that panic into a clean, readable test failure rather
// than crashing the whole test binary, so this test can show its own red
// phase without taking every other test in the package down with it.
func TestReportGivenANilCounterSaysSoInsteadOfPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Report(nil) panicked instead of naming the nil counter: %v", r)
		}
	}()

	got := Report(nil)
	want := "leak: a nil counter was given, so this check proved nothing"
	if len(got) != 1 || got[0] != want {
		t.Errorf("Report(nil) = %v, want [%q]", got, want)
	}
}
