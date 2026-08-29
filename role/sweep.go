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
// Compare this pass's trace against the IMMEDIATELY PREVIOUS pass's, and stop
// the comparison at min(n-1, len(trace), len(prev)).
//
// The bound does two jobs at once, and they coincide. It stops at what the
// current pass has not yet faulted, AND at what the previous pass had not yet
// faulted -- pass n-1's injection point is index n-2, so indices 0..n-2 are
// exactly its pre-fault operations plus the faulted operation itself.
//
// That is why the reference must be the previous pass and not pass 1. Pass 1
// faults operation 1, so its injection point is index 0 and everything after
// that in its trace is post-fault. Pass 1 is a valid reference for index 0 and
// for nothing else. Comparing against it keeps the first job and loses the
// second. (Transitivity recovers most of the rest: index k is compared at every
// pass from k+2 onward, so pairwise agreement chains back. The one index
// compared only once is the newest at the final pass.)
//
// Beyond the injection point the sequences legitimately diverge, because the
// fault changed what happened next -- compare whole traces and every sweep
// fails at pass two.
//
// A SHORTENING trace is the case min() hides, and it is instability rather than
// an artefact. If the fault fired on operation n then n operations were
// attempted, so the target's trace must hold at least n entries; fewer means
// the role did less work for a reason unrelated to the fault. Check it
// explicitly, because min() silently compares fewer indices and passes:
//
//	if p.hasFired() && len(trace) < n { ... }
//
// The graphdb implementation does not have this check. The argument for it is
// theirs and it has not been run there either, so treat it as reasoned rather
// than measured -- and note that a trace shorter than n while the point did NOT
// fire is the termination condition, already handled below.
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
