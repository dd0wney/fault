package crash_test

import (
	"testing"

	"github.com/dd0wney/fault/crash"
)

// A generator that quietly stops producing states is otherwise invisible,
// because fewer subtests still all pass. The number below is written down
// after running the test once and reading what it printed -- never from
// memory, and never before the run.
//
// The brief for this test named "inPlace" as the record to measure, but
// record() (properties_dirsync_test.go) takes a saveFunc (fsys, dir, value)
// and run_test.go's inPlace has the shape (t) (*crash.Recorder, string) -- a
// different helper entirely, already used by
// TestAnInPlaceWriteProducesEveryTornState. inPlace(t) is called directly
// here, matching what it already returns.
//
// inPlace calls no syncDir, so this test needs no build constraint, unlike
// the two properties that measure a safeStore record: those moved to
// properties_dirsync_test.go, behind //go:build !windows.
func TestTheStateCountForAKnownRecord(t *testing.T) {
	rec, _ := inPlace(t)

	got, err := crash.Plan(rec, crash.Model{})
	if err != nil {
		t.Fatal(err)
	}

	// MEASURED, not guessed. Run `go test -run TestTheStateCountForAKnownRecord
	// -v` once, read the count it reports, and write it here. If this number
	// changes, either the generator changed or a defect arrived; both need a
	// human to look.
	const want = 2 // measured: `go test ./crash/ -run TestTheStateCountForAKnownRecord -v`
	if want == 0 {
		t.Fatalf("the measured state count is missing: this record produces %d states — write that number into the const above", len(got))
	}
	if len(got) != want {
		t.Errorf("got %d states, want %d", len(got), want)
	}
}
