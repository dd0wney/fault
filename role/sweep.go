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
// what came back. Four of the five outcomes are the core package's: the caller
// broke out, the point fired, nothing fired on pass 1, nothing fired later. The
// fifth is this package's reason to exist.
//
// # Why stability is checked and not assumed
//
// A per-role counter is meaningful only while the role's own sequence is the
// same on every run, and that is a property of the CALLER'S SCENARIO rather
// than of this package. It cannot be verified once at the start and assumed
// after, so it is re-checked on every pass.
//
// # Why the reference is the previous pass
//
// The comparison stops at min(n-1, len(trace), len(prev)), and that bound does
// two jobs which happen to coincide. It stops at what this pass has not yet
// faulted, and at what the previous pass had not yet faulted -- pass n-1
// faulted its operation at index n-2, so indices 0..n-2 are exactly its
// pre-fault operations plus the faulted one.
//
// Pass 1 would not serve. It faults operation 1, so its injection point is
// index 0 and everything after that in its trace is post-fault: a valid
// reference for index 0 and for nothing else. Using it would keep the first job
// and lose the second.
//
// Transitivity recovers most of what pairwise comparison gives up: index k is
// compared at every pass from k+2 onward, so agreements chain back. The one
// index compared exactly once is the newest at the final pass.
//
// Beyond the injection point the sequences legitimately diverge, because the
// fault changed what happened next. Comparing whole traces fails every sweep
// with a cleanup path at pass two.
//
// # Why a shortening trace needs its own check
//
// A shorter trace makes min() compare fewer indices, find them equal, and pass,
// so the prefix comparison cannot see a role that simply did less work.
//
// The signal is at the TERMINATING pass. The previous pass faulted operation
// n-1, so the role performs at least n-1 operations; fewer here means it
// shortened. Two nearby formulations do not work. Comparing against the
// previous trace's LENGTH is wrong, because a terminating pass runs no cleanup
// and is legitimately shorter than every faulting pass before it. And checking
// it on a pass that DID fire is unreachable, because Op appends to the trace
// before testing armed, so a fired pass holds exactly n entries by
// construction.
//
// # The message
//
// Both diagnostics name the position and the counts, not only the operations.
// When this fires the message is the entire deliverable, and a reader with the
// names but not the position still has to find it by hand.
//
// The bound belongs here rather than in an adapter: it is a fact about what a
// sweep does, and an adapter author would get it wrong.
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

		// The stability check. Compare against the immediately previous pass,
		// stopping at the bound: it is simultaneously what this pass has not
		// yet faulted and what the previous pass had not yet faulted, because
		// pass n-1 faulted its operation at index n-2.
		if prev != nil {
			for i := range min(n-1, min(len(trace), len(prev))) {
				if trace[i] == prev[i] {
					continue
				}
				return fmt.Errorf("%w: role %q did %q as operation %d of pass %d, "+
					"and %q in the previous pass. \"The N-th operation\" therefore names "+
					"a different operation on each run, so this sweep proves nothing",
					errUnstable, target, trace[i], i+1, n, prev[i])
			}
		}

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
			// Nothing fired, so this is either the end of the role's sequence
			// or the role doing less work than last time. The previous pass
			// faulted operation n-1, so the role performs at least n-1
			// operations; a shorter trace here means it shortened.
			//
			// This is the case the prefix comparison above cannot see: a
			// shorter trace makes min() compare fewer indices, find them equal,
			// and pass. The obvious alternative -- comparing against the
			// previous trace's length -- is wrong, because a terminating pass
			// runs no cleanup and is legitimately shorter than every faulting
			// pass before it.
			if len(trace) < n-1 {
				return fmt.Errorf("%w: role %q performed %d operations in pass %d "+
					"and at least %d in the pass before, so the sweep stopped early "+
					"believing the sequence had ended. It had not: the role did less "+
					"work for a reason unrelated to the fault, and this sweep proves "+
					"nothing", errUnstable, target, len(trace), n, n-1)
			}
			return nil
		}
	}
}
