package alloc_test

import (
	"errors"
	"testing"

	"github.com/dd0wney/fault"
	faultalloc "github.com/dd0wney/fault/alloc"
)

// counting records what reached the real allocator.
type counting struct {
	allocs, frees int
}

func (c *counting) Bytes(n int) ([]byte, error) { c.allocs++; return make([]byte, n), nil }
func (c *counting) Free([]byte)                 { c.frees++ }

// Contract rule 3: every allocation must call Trip. The sweep is the counter --
// a scenario performing exactly one allocation runs exactly two passes.
func TestEveryAllocationCallsTrip(t *testing.T) {
	passes := 0
	for _, p := range fault.Sweep(t) {
		passes++
		a := faultalloc.New(p, faultalloc.Go())
		if b, err := a.Bytes(8); err == nil {
			a.Free(b)
		}
	}
	if passes != 2 {
		t.Errorf("%d passes, want 2", passes)
	}
}

// Contract rule 2: a refused allocation must not reach the real allocator.
// Contract rule 4: it must report this package's own error, which is the one a
// caller handles.
func TestARefusedAllocationIsNotPerformed(t *testing.T) {
	base := &counting{}
	for n, p := range fault.Sweep(t) {
		b, err := faultalloc.New(p, base).Bytes(8)
		if n != 1 {
			continue
		}
		if !errors.Is(err, faultalloc.ErrNoMemory) {
			t.Errorf("err = %v, want ErrNoMemory", err)
		}
		if b != nil {
			t.Errorf("got a %d-byte buffer, want nil", len(b))
		}
		if base.allocs != 0 {
			t.Errorf("the real allocator ran %d times, want 0", base.allocs)
		}
	}
}

// SQLite's first loop: refuse once, then behave normally. A handler that copes
// with a single failure and continues must be able to allocate again.
func TestNewRefusesOnlyOnce(t *testing.T) {
	for n, p := range fault.Sweep(t) {
		if n != 1 {
			continue
		}
		a := faultalloc.New(p, faultalloc.Go())

		if _, err := a.Bytes(8); !errors.Is(err, faultalloc.ErrNoMemory) {
			t.Fatalf("first allocation: err = %v, want ErrNoMemory", err)
		}
		if _, err := a.Bytes(8); err != nil {
			t.Errorf("second allocation: err = %v, want nil: New refuses once", err)
		}
	}
}

// SQLite's second loop, and the reason it exists: it finds a cleanup path that
// allocates in order to clean up. Once a refusal has happened, every later
// allocation must be refused too.
func TestAllFromRefusesEverySubsequentAllocation(t *testing.T) {
	for n, p := range fault.Sweep(t) {
		if n != 1 {
			continue
		}
		a := faultalloc.NewAllFrom(p, faultalloc.Go())

		if _, err := a.Bytes(8); !errors.Is(err, faultalloc.ErrNoMemory) {
			t.Fatalf("first allocation: err = %v, want ErrNoMemory", err)
		}
		for i := 2; i <= 4; i++ {
			if _, err := a.Bytes(8); !errors.Is(err, faultalloc.ErrNoMemory) {
				t.Errorf("allocation %d: err = %v, want ErrNoMemory: "+
					"NewAllFrom refuses everything after the first refusal", i, err)
			}
		}
	}
}

// Assertion 2 of the fault-injection loop, made free by the adapter. Without
// it, every unwind path in the code under test can leak while the sweep still
// reports a clean walk.
func TestOutstandingReportsBuffersNotFreed(t *testing.T) {
	for n, p := range fault.Sweep(t) {
		if n != 1 {
			continue
		}
		a := faultalloc.New(p, faultalloc.Go())

		if got := a.Outstanding(); got != 0 {
			t.Errorf("a fresh Fault reports %d outstanding, want 0", got)
		}

		// The refused allocation must not count: nothing was handed out.
		if _, err := a.Bytes(8); err == nil {
			t.Fatal("expected the first allocation to be refused")
		}
		if got := a.Outstanding(); got != 0 {
			t.Errorf("after a refusal: %d outstanding, want 0", got)
		}

		b1, _ := a.Bytes(8)
		b2, _ := a.Bytes(8)
		if got := a.Outstanding(); got != 2 {
			t.Errorf("after two allocations: %d outstanding, want 2", got)
		}

		a.Free(b1)
		if got := a.Outstanding(); got != 1 {
			t.Errorf("after one free: %d outstanding, want 1", got)
		}
		a.Free(b2)
		if got := a.Outstanding(); got != 0 {
			t.Errorf("after both frees: %d outstanding, want 0", got)
		}
	}
}

// Free must reach the real allocator, so a pooled implementation can reclaim.
func TestFreeReachesTheRealAllocator(t *testing.T) {
	base := &counting{}
	for n, p := range fault.Sweep(t) {
		if n != 1 {
			continue
		}
		a := faultalloc.New(p, base)
		a.Bytes(8) // refused on this pass
		b, err := a.Bytes(8)
		if err != nil {
			t.Fatalf("second allocation: %v", err)
		}
		a.Free(b)
		if base.frees != 1 {
			t.Errorf("the real Free ran %d times, want 1", base.frees)
		}
	}
}

// The policy for an unmatched Free, pinned. Without this test, a negative
// Outstanding reads like a defect and the obvious "fix" -- clamping at zero --
// passes the whole suite while destroying the only signal that reports a double
// free or a Free on a refused allocation.
func TestAnUnmatchedFreeDrivesTheCountNegative(t *testing.T) {
	for n, p := range fault.Sweep(t) {
		if n != 1 {
			continue
		}
		a := faultalloc.New(p, faultalloc.Go())

		b, err := a.Bytes(8)
		if err == nil {
			t.Fatal("expected the first allocation of pass 1 to be refused")
		}

		// The deferred-Free-before-the-error-check pattern, which runs on
		// exactly the pass where the allocation was refused.
		a.Free(b)
		if got := a.Outstanding(); got != -1 {
			t.Errorf("freeing a buffer that was never allocated: outstanding = %d, want -1", got)
		}

		c, _ := a.Bytes(8)
		a.Free(c)
		a.Free(c) // an ordinary double free
		if got := a.Outstanding(); got != -2 {
			t.Errorf("after a double free: outstanding = %d, want -2", got)
		}
	}
}

// failingAlloc is a base that refuses on its own.
type failingAlloc struct{ err error }

func (f failingAlloc) Bytes(int) ([]byte, error) { return nil, f.err }
func (f failingAlloc) Free([]byte)               {}

// A real refusal from the base is not a fault this package chose, and it must
// reach the code under test unchanged.
func TestARealErrorFromTheBasePassesThrough(t *testing.T) {
	want := errors.New("the pool is empty")
	for n, p := range fault.Sweep(t) {
		a := faultalloc.New(p, failingAlloc{err: want})
		_, err := a.Bytes(8)
		if n == 1 {
			continue // pass 1 refuses before reaching the base
		}
		if !errors.Is(err, want) {
			t.Errorf("pass %d: err = %v, want the base's own error", n, err)
		}
	}
}

// NewAllFrom must refuse from its ARMED point, not from the first allocation.
// Recording the refusal without checking that Trip fired makes every allFrom
// adapter refuse everything, and the pass-1 tests above cannot see it because
// on pass 1 the first allocation is the armed one.
func TestAllFromRefusesNothingBeforeItsArmedPoint(t *testing.T) {
	// Every pass must perform the allocations, or the sweep sees a scenario
	// that did nothing and refuses. Only the assertions are conditional.
	for n, p := range fault.Sweep(t) {
		a := faultalloc.NewAllFrom(p, faultalloc.Go())

		var errs [4]error
		for i := range errs {
			_, errs[i] = a.Bytes(8)
		}
		if n != 3 {
			continue // the armed point is the third allocation
		}

		for i := 0; i < 2; i++ {
			if errs[i] != nil {
				t.Errorf("allocation %d: err = %v, want nil: the armed point is 3", i+1, errs[i])
			}
		}
		if !errors.Is(errs[2], faultalloc.ErrNoMemory) {
			t.Errorf("allocation 3: err = %v, want ErrNoMemory", errs[2])
		}
		if !errors.Is(errs[3], faultalloc.ErrNoMemory) {
			t.Errorf("allocation 4: err = %v, want ErrNoMemory: every one after the refusal", errs[3])
		}
	}
}
