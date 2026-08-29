package role

import (
	"errors"
	"fmt"
	"iter"
	"testing"
)

// maxOps bounds the walk, so a scenario that performs more work on each pass
// cannot loop forever.
const maxOps = 512

var (
	errNoOperations  = errors.New("role: the role performed no operations, so the sweep proved nothing")
	errNoTermination = errors.New("role: the sweep did not terminate")
	errUnstable      = errors.New("role: the role's operation sequence is not stable")
)

// Sweep walks the point of failure through every operation the target role
// performs, while every other role runs untouched.
//
// The loop body runs the scenario and asserts the invariant, exactly as
// [github.com/dd0wney/fault.Sweep] does. When the body fails a pass, Sweep
// prints the structural [Key] of the operation that failed, so the finding can
// be pinned as a regression test that depends on neither an ordinal nor a
// schedule.
func Sweep(t *testing.T, target Role) iter.Seq2[int, *Points] {
	return func(yield func(int, *Points) bool) {
		t.Helper()
		if err := walk(t, target, yield); err != nil {
			t.Fatal(err)
		}
	}
}

// walk runs the sweep and returns a diagnostic rather than failing a test.
//
// For n = 1, 2, 3, ... it arms the target's n-th operation, yields, and reads
// what came back. Four of the five cases are the same as the core package's:
// the caller broke out, the point fired, nothing fired on pass 1, nothing fired
// later. The fifth is this package's reason to exist.
//
// TODO(ddowney): the stability check, between the yield and the termination
// test.
//
// A per-role counter is only meaningful while the role's own sequence is the
// same on every run, and that is a property of the CALLER'S SCENARIO, not of
// this package. It cannot be verified once at the start and assumed after: it
// has to be re-checked on every pass.
//
// Compare this pass's trace against the previous pass's, and stop the
// comparison at min(n-1, len(trace), len(prev)). The bound is the whole trick.
// Beyond the injection point the two sequences legitimately diverge, because
// the fault changed what happened next -- compare whole traces and every sweep
// fails at pass two.
//
// On a divergence, wrap errUnstable with a message naming the position, both
// operations, and what it means. That message is the entire deliverable when
// this fires, and the reader has to be able to act on it:
//
//	role "flush" is not stable: at operation 3 of pass 5 it did "sync wal.log",
//	and in the previous pass it did "write wal.log". "The N-th operation" names
//	a different operation on each run, so this sweep proves nothing.
//
// The bound belongs here and not in an adapter. It is a fact about what a sweep
// does, and an adapter author would get it wrong.
func walk(t *testing.T, target Role, yield func(int, *Points) bool) error {
	t.Helper()

	var prev []string
	for n := 1; ; n++ {
		if n > maxOps {
			return fmt.Errorf("%w within %d operations of role %q: the scenario performs "+
				"more work on each pass, or the adapter does not call Op",
				errNoTermination, maxOps, target)
		}

		p := newPoints(target, n)
		failedBefore := t.Failed()
		if !yield(n, p) {
			return nil
		}

		trace := p.traceOf()

		// TODO(ddowney): the stability check goes here, comparing trace
		// against prev up to the bound described above.
		_ = prev // remove when the check reads it

		prev = trace

		// A failing pass reports the structural key, so the finding can be
		// pinned without depending on the ordinal or the schedule.
		if !failedBefore && t.Failed() {
			if k, ok := p.firedKey(); ok {
				t.Logf("failure point %d has structural key %s", n, k)
			}
		}

		if !p.hasFired() {
			if n == 1 {
				return fmt.Errorf("%w: role %q", errNoOperations, target)
			}
			return nil
		}
	}
}
