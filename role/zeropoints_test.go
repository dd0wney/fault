package role_test

import (
	"testing"

	"github.com/dd0wney/fault/role"
)

// A pass must fail AT MOST one operation, and the last pass must fail none.
//
// This is the role package's form of "the adapter invents no failure". The
// other adapters can be handed a zero fault.Points; a role.Points is built by
// the sweep and has no usable zero value, so the property is stated over the
// whole walk instead:
//
//   - No pass may report true from more than one Op. More than one would be a
//     second failure nobody armed.
//   - The final pass arms an operation the scenario never reaches, so it must
//     report true from none. A sweep whose last pass still failed something
//     would never terminate honestly.
//
// THE GAP THIS CLOSES WAS FOUND IN fault/sql ON 2026-08-31, and not by reading
// this package. rows.Next held it: turning `if r.armed && r.n == r.at` into
// `if true && r.n == r.at` changed no test result, and that version fails the
// first row of every result set, armed or not. Every test in that suite armed
// something, so none of them could see it.
//
// An invented failure is worse than a missed one. A missed injection
// under-reports and its report stays true. An invented one reports a defect
// the code under test does not have, so every later finding needs a person to
// re-derive whether it is real.
func TestNoPassFailsMoreThanOneOperationAndTheLastFailsNone(t *testing.T) {
	const target = role.Role("flush")

	passes, lastPassFailures := 0, -1
	for _, p := range role.Sweep(t, target) {
		passes++
		failures := 0

		// A fixed three-operation scenario for the target, with another actor
		// interleaved. The other actor must never fail at all.
		for _, op := range []string{"write wal", "sync wal", "rename wal"} {
			if p.Op(target, op) {
				failures++
			}
			if p.Op("compact", "write sst") {
				t.Errorf("pass %d failed an operation belonging to 'compact', which is not the target", passes)
			}
		}

		if failures > 1 {
			t.Errorf("pass %d failed %d operations, want at most 1 — a pass arms one operation", passes, failures)
		}
		lastPassFailures = failures
	}

	if passes < 2 {
		t.Fatalf("the sweep produced %d pass(es), so there was no final pass to assert about", passes)
	}
	if lastPassFailures != 0 {
		t.Errorf("the final pass failed %d operation(s), want 0 — it arms an operation the scenario never reaches", lastPassFailures)
	}
}
