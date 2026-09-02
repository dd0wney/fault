package fault

import (
	"errors"
	"fmt"
	"iter"
	"testing"
)

// maxOps bounds the walk, so a scenario that performs more work on each pass
// cannot loop forever. Exceeding it fails the test rather than passing
// quietly, because a sweep that never reaches its end has proved nothing.
//
// MEASURED 2026-09-02: the largest sweep the graphdb harness produces is 52
// points (its eio/wal+edges scenario), a tenth of this bound, and no sweep
// there has hit it. A caller cannot raise the bound, by design: a scenario
// near it is a scenario that should be split, not a bound that should grow.
const maxOps = 512

// The two ways a sweep can fail to prove anything. Both are failures of the
// sweep itself rather than of the code under test.
//
// walk returns these instead of failing a test, so that a test can reach them.
// A test cannot observe t.Fatal on its own *testing.T, and testing.TB carries
// an unexported method, so nothing outside the testing package can supply a
// stand-in. Without this split the two most important error paths in the
// package would be unreachable from any test.
var (
	errNoOperations  = errors.New("fault: the scenario performed no operations, so the sweep proved nothing")
	errNoTermination = errors.New("fault: the sweep did not terminate")
)

// Sweep yields the operation number to arm, and the Points that carries it.
//
// The loop body runs the scenario and asserts the invariant:
//
//	for n, p := range fault.Sweep(t) {
//		fs := faultfs.New(p)
//
//		err := store.OpenAndWrite(fs)
//
//		if !reopens(t, dir) {
//			t.Errorf("op %d: the store did not reopen: %v", n, err)
//		}
//	}
//
// Sweep arms the next point after each pass, and it ends the walk when the body
// completes without the fault firing: N has then run off the end of the
// sequence, so every error path the scenario can reach has been visited.
//
// Report a broken invariant with [testing.T.Errorf] to walk the whole sequence
// and see every point that fails, or with [testing.T.Fatalf] to stop at the
// first one. Sweep takes no position on which is right.
func Sweep(t *testing.T) iter.Seq2[int, *Points] {
	return func(yield func(int, *Points) bool) {
		t.Helper()
		if err := walk(yield); err != nil {
			t.Fatal(err)
		}
	}
}

// walk runs the sweep and returns a diagnostic rather than failing a test.
//
// For n = 1, 2, 3, ... it arms point n, yields it, and reads what came back:
//
//   - yield returned false. The caller used break, which is their right, and
//     that is not a diagnostic.
//   - the point fired. There is more sequence to walk, so continue to n+1.
//   - the point did not fire on pass 1. The scenario performed no operations at
//     all, so the sweep proved nothing.
//   - the point did not fire on any later pass. The fault ran off the end of
//     the sequence and the walk is complete.
//
// The third case is the package's own negative control and the most valuable
// branch in it. A sweep over a scenario that performs no I/O terminates at once
// and passes, which reads exactly like a sweep that visited every error path
// and found nothing wrong.
//
// n passing maxOps means the walk did not terminate. The diagnostic names the
// bound and both causes, because the two are indistinguishable from in here:
// either the scenario performs more work on each pass, or an adapter does not
// call Trip.
//
// For a scenario performing K operations, where K >= 1, this yields exactly
// K+1 passes: one that fails each operation in turn, plus the final pass that
// proves the sequence ended. The final pass is not an off-by-one. Without it a
// sweep could stop early and still look correct.
func walk(yield func(int, *Points) bool) error {
	for n := 1; ; n++ {
		if n > maxOps {
			return fmt.Errorf("%d %w", maxOps, errNoTermination)
		}
		points := Points{armed: n}
		if !yield(n, &points) {
			return nil
		}
		if !points.hasFired() {
			if n == 1 {
				return errNoOperations
			}
			return nil
		}
	}
}
