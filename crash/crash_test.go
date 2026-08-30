package crash_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// The three values the scenario publishes.
//
// They are four bytes each, and two consecutive values differ in TWO byte
// positions. That is what makes a torn write visible. With "v2" and "v3" the
// two values differ in one byte only, so every partial write of one over the
// other lands on a value the check would accept, and inPlaceStore -- whose
// whole defect is the torn write -- would pass the table while proving
// nothing.
//
// The three lengths are equal for a second reason. Recorder.OpenFile records
// O_TRUNC as a plain open, so the replay does not model the size reset. A
// value shorter than the one it replaces would leave the tail of the older
// value behind, the replay control would refuse the whole record, and every
// row would fail for a reason no store caused.
const (
	v1 = "v1v1"
	v2 = "v2v2"
	v3 = "v3v3"
)

// maxReported bounds how many broken states one row prints. The first few name
// the defect; the rest repeat it.
const maxReported = 4

// breach is one candidate state that broke the invariant.
//
// It carries the state's go test -run address, so a reader can re-run the one
// state, and a reason that names which finding it is. "The store did not
// reopen" and "the value was wrong" both otherwise arrive as a bare false, and
// they call for different work.
type breach struct {
	state string
	why   string
}

func (b breach) String() string { return b.state + ": " + b.why }

// survives runs one reference store under one model and reports whether every
// candidate state kept the invariant.
//
// It uses States rather than Run, because a test that asserts a store FAILS
// cannot let that failure reach the enclosing *testing.T.
//
// # Two saves, not one
//
// The second save is the whole discriminating power of this table. With a
// single save the old value is a legal answer at every crash point -- the
// crash simply happened before the save finished -- so losing the rename is
// permitted and noDirSync SURVIVES the POSIX model. The one row able to catch
// Model.MetadataDurable being read and then ignored would go green while
// proving nothing, which is a false negative and looks exactly like success.
//
// # The invariant is per crash point
//
// It is NOT "the value is v2 or v3" at every state, which is what a first
// reading of the two-save scenario suggests. A crash inside the FIRST save may
// legally leave v1, because nothing has been acknowledged yet. safeStore
// produces exactly that state -- crash after the rename of v2, lose the
// rename, read v1 -- so the flat rule fails correct code. That is the worst
// outcome this package can have: a crash simulator that flags a correct store
// makes every later finding need a human to re-derive whether it is real.
//
// The rule is:
//
//	a crash inside the first save   -> v1 or v2
//	a crash inside the second save  -> v2 or v3, and never v1
//
// The second line is the real assertion. save(v2) RETURNED before the second
// save began, so a state built from a crash point inside the second save that
// reads v1 has reverted acknowledged data. State.Point is what lets the check
// tell the two halves apart.
//
// # A known limit of that rule
//
// plan drops duplicate trees by fingerprint across ALL crash points, so one
// tree reachable from two points arrives under the EARLIER one. The rule above
// is therefore sound but not complete: it never calls a legal state a defect,
// and it can miss an illegal one whose tree also appears earlier, where v1 is
// still legal. The bias runs the safe way -- towards a missed finding and away
// from a false positive on correct code -- and the reference table still fails
// every store it must, because their reverting trees carry the second save's
// temporary file and no early state does. See the task report.
func survives(t *testing.T, save saveFunc, m crash.Model) (bool, []breach) {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	if err := save(rec, dir, v2); err != nil {
		t.Fatalf("the first save failed while recording: %v\n"+
			"No store can be measured against a run it did not finish. On Windows the "+
			"cause is syncDir, because a directory handle cannot be synced there. Task 14 "+
			"splits the build for that platform.", err)
	}

	// Every entry so far belongs to the first save, so a crash point after
	// this index is unambiguously after save(v2) returned.
	acked := len(crash.Entries(rec))

	if err := save(rec, dir, v3); err != nil {
		t.Fatalf("the second save failed while recording: %v", err)
	}

	states, err := crash.States(rec, m)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// The positive control for the rule above. The never-revert half of the
	// invariant only says anything about states whose crash point lies inside
	// the second save, and a walk that produced none of those would assert
	// nothing while reporting a pass -- which is the failure this whole package
	// exists to catch, wearing the test's own clothes.
	late := 0
	for _, s := range states {
		if s.Point > acked {
			late++
		}
	}
	t.Logf("%d entries recorded, %d of them in the first save; %d candidate states, %d of them from a crash in the second save",
		len(crash.Entries(rec)), acked, len(states), late)
	if late == 0 {
		t.Fatal("no candidate state has a crash point inside the second save, so the never-revert rule asserted nothing")
	}

	var out []breach
	for _, s := range states {
		older, newer := v1, v2
		if s.Point > acked {
			older, newer = v2, v3
		}

		got, readErr := readStore(s.FS(t), dir)
		if readErr != nil {
			out = append(out, breach{
				state: s.Name,
				why:   fmt.Sprintf("the store did not reopen: %v", readErr),
			})
			continue
		}
		if got != older && got != newer {
			out = append(out, breach{
				state: s.Name,
				why:   fmt.Sprintf("read %q, want %q or %q", got, older, newer),
			})
		}
	}
	return len(out) == 0, out
}

// TestTheFourReferenceStores is the acceptance test for this package. It is
// the claim itself: that crash can tell a store that survives a power cut from
// one that does not.
//
// A row can fail in two directions, and they mean opposite things.
//
// A store that must survive and does not is a FALSE POSITIVE. Do not relax the
// check to make it pass. Read the states the row prints and find out which
// rule built them.
//
// A store that must fail and does not is a false negative. noDirSync/posix is
// the row to read first: if it reports survives = true, the durability model
// is not separating a file sync from a directory sync, and the fix belongs in
// split, not here.
func TestTheFourReferenceStores(t *testing.T) {
	cases := []struct {
		name     string
		save     saveFunc
		model    crash.Model
		survives bool
	}{
		{"safeStore/posix", safeStore, crash.Model{}, true},
		{"safeStore/metadataDurable", safeStore, crash.Model{MetadataDurable: true}, true},
		{"noFileSync/posix", noFileSync, crash.Model{}, false},
		{"noFileSync/metadataDurable", noFileSync, crash.Model{MetadataDurable: true}, false},
		{"noDirSync/posix", noDirSync, crash.Model{}, false},
		{"noDirSync/metadataDurable", noDirSync, crash.Model{MetadataDurable: true}, true},
		// Sector: 1 is what makes the torn write a unit. Under the whole-call
		// model an in-place write is lost entirely or not at all, and both of
		// those states hold a legal value.
		{"inPlaceStore/posix", inPlaceStore, crash.Model{Sector: 1}, false},
		{"inPlaceStore/metadataDurable", inPlaceStore, crash.Model{Sector: 1, MetadataDurable: true}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, breaches := survives(t, c.save, c.model)

			for i, b := range breaches {
				if i == maxReported {
					t.Logf("and %d more states broke the invariant", len(breaches)-i)
					break
				}
				t.Logf("broke the invariant: %s", b)
			}

			if got != c.survives {
				t.Errorf("survives = %v, want %v", got, c.survives)
			}
		})
	}
}
