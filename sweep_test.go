package fault_test

import (
	"errors"
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

// --- Metamorphic properties (spec section 13.4) ---
//
// These catch the failure mode the example tests cannot: a walk that stops one
// pass early still passes every example test above.
func TestWalkVisitsEachOperationExactlyOnce(t *testing.T) {
	for ops := 1; ops <= 8; ops++ {
		var passes int
		var tripped []int

		err := fault.Walk(func(n int, p *fault.Points) bool {
			passes++
			for op := 1; op <= ops; op++ {
				if p.Trip() {
					tripped = append(tripped, op)
					break // a real adapter returns an error here
				}
			}
			return true
		})
		if err != nil {
			t.Fatalf("ops=%d: %v", ops, err)
		}

		// One pass fails each operation in turn, plus the pass that proves the
		// sequence ended.
		if passes != ops+1 {
			t.Errorf("ops=%d: %d passes, want %d", ops, passes, ops+1)
		}

		want := make([]int, ops)
		for i := range want {
			want[i] = i + 1
		}
		if !slices.Equal(tripped, want) {
			t.Errorf("ops=%d: failed operations %v, want %v", ops, tripped, want)
		}
	}
}
