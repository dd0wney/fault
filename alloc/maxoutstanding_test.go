package alloc_test

import (
	"testing"

	"github.com/dd0wney/fault"
	"github.com/dd0wney/fault/alloc"
)

// The reading this method exists for, stated as the discrimination itself.
//
// Outstanding() == 0 is the pass condition for "nothing leaked". It is also
// exactly what a scenario that never allocated returns. These two scenarios are
// indistinguishable by Outstanding alone, and MaxOutstanding is the only thing
// that separates them.
//
// Note what this does NOT drive through fault.Sweep. Free performs no
// operation, so a scenario of Frees alone makes the sweep refuse -- "the
// scenario performed no operations, so the sweep proved nothing" -- which is
// the sweep's own negative control working, and it is the wrong instrument for
// this question. A zero Points is the right one.
func TestOnlyMaxOutstandingSeparatesCleanFromNeverAllocated(t *testing.T) {
	neverAllocated := alloc.New(&fault.Points{}, alloc.Go())

	acquiredAndReleased := alloc.New(&fault.Points{}, alloc.Go())
	b, err := acquiredAndReleased.Bytes(8)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	acquiredAndReleased.Free(b)

	// Indistinguishable by the leak count, which is the whole problem.
	if neverAllocated.Outstanding() != acquiredAndReleased.Outstanding() {
		t.Fatalf("Outstanding separates the two scenarios (%d and %d), so this test is "+
			"measuring something other than the case the method exists for",
			neverAllocated.Outstanding(), acquiredAndReleased.Outstanding())
	}

	// And separated by the high-water mark.
	if got := neverAllocated.MaxOutstanding(); got != 0 {
		t.Errorf("nothing was allocated, MaxOutstanding = %d, want 0", got)
	}
	if got := acquiredAndReleased.MaxOutstanding(); got != 1 {
		t.Errorf("one buffer was taken and returned, MaxOutstanding = %d, want 1", got)
	}
}

// The high-water mark rises with the count and does not fall with it.
func TestMaxOutstandingHoldsTheHighWaterMark(t *testing.T) {
	a := alloc.New(&fault.Points{}, alloc.Go())

	first, err := a.Bytes(8)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	second, err := a.Bytes(8)
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	if got := a.MaxOutstanding(); got != 2 {
		t.Fatalf("MaxOutstanding = %d while holding two buffers, want 2", got)
	}

	a.Free(first)
	a.Free(second)

	if got := a.Outstanding(); got != 0 {
		t.Errorf("Outstanding = %d after freeing both, want 0", got)
	}
	if got := a.MaxOutstanding(); got != 2 {
		t.Errorf("MaxOutstanding = %d after freeing both, want 2 — a high-water mark "+
			"that falls answers the same question Outstanding already answers", got)
	}
}

// A refused allocation hands nothing out, so it must not move the mark.
//
// This is the rule Outstanding already follows, and a high-water mark that
// counted refusals would report that a scenario held a buffer it was never
// given -- which would make the "was the question live" reading answer yes for
// a scenario in which every allocation failed.
func TestARefusedAllocationDoesNotMoveTheMark(t *testing.T) {
	var refusedAtLeastOnce bool
	for n, p := range fault.Sweep(t) {
		a := alloc.New(p, alloc.Go())

		if _, err := a.Bytes(8); err != nil {
			refusedAtLeastOnce = true
			if got := a.MaxOutstanding(); got != 0 {
				t.Errorf("point %d: the only allocation was refused, MaxOutstanding = %d, want 0", n, got)
			}
			continue
		}
		if got := a.MaxOutstanding(); got != 1 {
			t.Errorf("point %d: one buffer was handed out, MaxOutstanding = %d, want 1", n, got)
		}
	}

	// The positive control. If no pass ever refused, the assertion above ran
	// over nothing and this test proves only that a successful Bytes counts.
	if !refusedAtLeastOnce {
		t.Fatal("no pass refused an allocation, so the refusal case was never measured")
	}
}
