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
// The three lengths are equal so that the torn write is the only way to reach a
// value outside the pair. A shorter value would also leave the tail of the one
// it replaced whenever a state lost the truncate, and that is a second defect
// wearing the same clothes. Keeping the lengths equal makes each broken state
// attributable to one cause.
const (
	v1 = "v1v1"
	v2 = "v2v2"
	v3 = "v3v3"
)

// maxReported bounds how many broken states one row prints. The first several
// name the defect, the rest repeat it, and the trailing count still states the
// real scale. inPlaceStore breaks 36 states, so an uncapped list would bury the
// three rows that break one or two.
const maxReported = 8

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
// The rule reads State.Point and needs it to be the crash point of THIS state,
// not of some earlier state that rebuilt the same bytes. plan deduplicates
// within one crash point and not across the walk, which is what makes that
// true. Sharing the fingerprint set across the walk collapsed a late state
// onto an early one, where v1 is still legal, and noDirSync/posix lost nine of
// its ten reverting states to it.
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

// refStoreCase is one row of the reference-store acceptance table. The type
// is shared, because the table itself is split in two: safeStore and
// noFileSync call syncDir, which cannot build on Windows, so their rows live
// in stores_dirsync_test.go behind //go:build !windows, and would not compile
// here if they stayed in this table.
type refStoreCase struct {
	name     string
	save     saveFunc
	model    crash.Model
	survives bool
}

// runReferenceStoreCases runs one half of the acceptance table and reports
// each row the same way, so the two halves cannot drift into different
// reporting rules.
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
func runReferenceStoreCases(t *testing.T, cases []refStoreCase) {
	t.Helper()
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

// TestTheFourReferenceStores is half of the acceptance test for this package:
// the claim itself, restricted to the two stores that need no directory sync
// and so build and run on every platform, Windows included. safeStore and
// noFileSync complete the claim in
// TestTheFourReferenceStoresThatSyncADirectory, which builds everywhere except
// Windows because syncDir cannot run there.
func TestTheFourReferenceStores(t *testing.T) {
	runReferenceStoreCases(t, []refStoreCase{
		{"noDirSync/posix", noDirSync, crash.Model{}, false},
		{"noDirSync/metadataDurable", noDirSync, crash.Model{MetadataDurable: true}, true},
		// Sector: 1 is what makes the torn write a unit. Under the whole-call
		// model an in-place write is lost entirely or not at all, and both of
		// those states hold a legal value.
		{"inPlaceStore/posix", inPlaceStore, crash.Model{Sector: 1}, false},
		{"inPlaceStore/metadataDurable", inPlaceStore, crash.Model{Sector: 1, MetadataDurable: true}, false},
	})
}
