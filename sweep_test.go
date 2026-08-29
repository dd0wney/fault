package fault_test

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dd0wney/fault"
)

var errInjected = errors.New("injected")

// resource stands in for a real adapter such as fault/fs. It performs a fixed
// number of operations, and it asks Points before each one whether this is the
// operation that must fail.
type resource struct {
	p   *fault.Points
	ops int
}

func (r *resource) run() error {
	for range r.ops {
		if r.p.Trip() {
			return errInjected
		}
	}
	return nil
}

// A scenario that performs three operations has four passes: one that fails
// each operation in turn, and a final one where the fault runs off the end.
func TestSweepVisitsEveryOperationThenStops(t *testing.T) {
	var armed []int
	var lastErr error

	for n, p := range fault.Sweep(t) {
		armed = append(armed, n)
		r := &resource{p: p, ops: 3}
		lastErr = r.run()
	}

	if want := []int{1, 2, 3, 4}; !slices.Equal(armed, want) {
		t.Errorf("armed %v, want %v", armed, want)
	}
	if lastErr != nil {
		t.Errorf("last pass returned %v, want nil: the fault must run off the end", lastErr)
	}
}

// Each pass must fail exactly one operation, and it must be the armed one.
func TestSweepFailsTheArmedOperation(t *testing.T) {
	for n, p := range fault.Sweep(t) {
		var failedAt int
		for i := 1; i <= 3; i++ {
			if p.Trip() {
				failedAt = i
				break
			}
		}
		if n <= 3 && failedAt != n {
			t.Errorf("pass %d failed operation %d, want %d", n, failedAt, n)
		}
	}
}

// break must end the walk without the iterator complaining.
func TestSweepStopsOnBreak(t *testing.T) {
	seen := 0
	for _, p := range fault.Sweep(t) {
		seen++
		r := &resource{p: p, ops: 10}
		_ = r.run()
		if seen == 2 {
			break
		}
	}
	if seen != 2 {
		t.Errorf("seen = %d, want 2", seen)
	}
}

// --- The two ways a sweep proves nothing (spec section 13.2) ---
//
// These drive Walk rather than Sweep, because Sweep reports them with t.Fatal
// and a test cannot observe t.Fatal on its own *testing.T.

// A scenario that performs no operations at all must be refused. The sweep
// would otherwise terminate on its first pass and report a pass, which reads
// exactly like a sweep that visited every error path and found nothing wrong.
func TestWalkRefusesAScenarioThatDoesNothing(t *testing.T) {
	passes := 0
	err := fault.Walk(func(n int, p *fault.Points) bool {
		passes++
		return true // the body performs no operations
	})

	if !errors.Is(err, fault.ErrNoOperations) {
		t.Errorf("err = %v, want ErrNoOperations", err)
	}
	if passes != 1 {
		t.Errorf("ran %d passes, want 1: the walk must stop as soon as it knows", passes)
	}
}

// A scenario that performs one more operation on each pass never runs off the
// end of its own sequence. The walk must give up and say so.
func TestWalkRefusesAWalkThatDoesNotTerminate(t *testing.T) {
	err := fault.Walk(func(n int, p *fault.Points) bool {
		for range n { // pass n performs n operations, so point n always fires
			p.Trip()
		}
		return true
	})

	if !errors.Is(err, fault.ErrNoTermination) {
		t.Errorf("err = %v, want ErrNoTermination", err)
	}
	if err != nil && !strings.Contains(err.Error(), strconv.Itoa(fault.MaxOps)) {
		t.Errorf("the diagnostic must name the bound %d: %v", fault.MaxOps, err)
	}
}

// break is the caller's right, and it is not a diagnostic.
func TestWalkReportsNothingWhenTheCallerStops(t *testing.T) {
	passes := 0
	err := fault.Walk(func(n int, p *fault.Points) bool {
		passes++
		// Perform enough operations that the walk would keep going on its own.
		// A body that performs one operation ends the walk at pass 2 because
		// the fault runs off the end, and then break is never the reason.
		for range 10 {
			if p.Trip() {
				break
			}
		}
		return passes < 3 // the range loop's break
	})

	if err != nil {
		t.Errorf("err = %v, want nil: break is not a failure", err)
	}
	if passes != 3 {
		t.Errorf("ran %d passes, want 3", passes)
	}
}

// --- The model (spec section 13.4) ---
//
// Everything walk does is a function of one number: how many operations the
// scenario performs. The model is two sentences, and the tests below check the
// implementation against it rather than against hand-written examples.
//
//	A scenario performing K operations yields exactly K+1 passes.
//	The operations that fail are exactly 1, 2, ... K, in order.
//
// These catch the failure the example tests cannot. A walk that drops its final
// pass satisfies every example above: it returns the right errors, it stops on
// break, and it refuses an empty scenario.

// model returns what walk must produce for a scenario of k operations.
func model(k int) (passes int, failed []int) {
	passes = k + 1
	failed = make([]int, k)
	for i := range failed {
		failed[i] = i + 1
	}
	return passes, failed
}

func TestWalkMatchesTheModel(t *testing.T) {
	for k := 1; k <= 64; k++ {
		var passes int
		var failed []int

		err := fault.Walk(func(n int, p *fault.Points) bool {
			passes++
			for op := 1; op <= k; op++ {
				if p.Trip() {
					failed = append(failed, op)
					break // a real adapter returns an error here
				}
			}
			return true
		})
		if err != nil {
			t.Fatalf("k=%d: %v", k, err)
		}

		wantPasses, wantFailed := model(k)
		if passes != wantPasses {
			t.Errorf("k=%d: %d passes, want %d", k, passes, wantPasses)
		}
		if !slices.Equal(failed, wantFailed) {
			t.Errorf("k=%d: failed operations %v, want %v", k, failed, wantFailed)
		}
	}
}

// Cleanup after a fault performs operations of its own, so the number of
// operations per pass is no longer constant. The model still holds, and the
// reason is worth stating: the pass that proves the sequence ended is the one
// where nothing fires, so no cleanup runs on it either.
//
// This is the shape of every real scenario -- close the file, remove the
// temporary, release the buffer -- and it is the case an exhaustive sweep
// exists to reach.
func TestCleanupOperationsDoNotChangeTheModel(t *testing.T) {
	for _, tc := range []struct{ base, cleanup int }{
		{1, 1}, {3, 2}, {8, 4}, {16, 1},
	} {
		var passes int
		var failed []int

		err := fault.Walk(func(n int, p *fault.Points) bool {
			passes++
			for op := 1; op <= tc.base; op++ {
				if p.Trip() {
					failed = append(failed, op)
					// The cleanup path, which performs operations of its own.
					for range tc.cleanup {
						p.Trip()
					}
					break
				}
			}
			return true
		})
		if err != nil {
			t.Fatalf("base=%d cleanup=%d: %v", tc.base, tc.cleanup, err)
		}

		wantPasses, wantFailed := model(tc.base)
		if passes != wantPasses {
			t.Errorf("base=%d cleanup=%d: %d passes, want %d",
				tc.base, tc.cleanup, passes, wantPasses)
		}
		if !slices.Equal(failed, wantFailed) {
			t.Errorf("base=%d cleanup=%d: failed %v, want %v",
				tc.base, tc.cleanup, failed, wantFailed)
		}
	}
}

// Sweep must fail the test when walk returns a diagnostic. That one line is the
// whole cost of the seam in sweep.go: walk is testable because it returns an
// error, and the line that turns the error into a failure is not.
//
// A test cannot contain a failing subtest and still pass, so this re-executes
// the test binary and asserts the child failed -- the pattern the standard
// library uses in os/exec's own tests. Deleting the t.Fatal in Sweep passes
// every other test in this package.
func TestSweepFailsTheTestWhenTheWalkProvesNothing(t *testing.T) {
	if os.Getenv("FAULT_SWEEP_CHILD") == "1" {
		// The child. A scenario that performs no operations must fail here.
		for range fault.Sweep(t) {
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "FAULT_SWEEP_CHILD=1")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("the child test passed: Sweep did not fail a walk that proved nothing\n%s", out)
	}
	if !strings.Contains(string(out), "proved nothing") {
		t.Errorf("the child failed without the diagnostic:\n%s", out)
	}
}
