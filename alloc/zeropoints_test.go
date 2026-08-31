package alloc_test

import (
	"testing"

	"github.com/dd0wney/fault"
	"github.com/dd0wney/fault/alloc"
)

// A zero Points arms nothing, so this adapter must refuse nothing.
//
// The core already proves that a zero Points never trips. This proves the
// adapter's own half: that it refuses an allocation ONLY because Trip said so,
// and never for a reason of its own.
//
// The gap this closes was found in fault/sql on 2026-08-31, not by reading
// this package. rows.Next held it: turning `if r.armed && r.n == r.at` into
// `if true && r.n == r.at` changed no test result, and that version fails the
// first row of every result set, armed or not. Every test in that suite armed
// something, so none could see it.
//
// UNLIKE fault/fs, THIS PACKAGE'S CONDITION IS COMPOUND. It writes
//
//	refuse := tripped || a.refusing
//
// so a mutant CAN express an invented failure here, and the package sits at a
// floor of 1.00. That is a reason to expect the gate to help and not a reason
// to skip the assertion: the score says the mutants go-mutesting generated
// were killed, and it says nothing about the ones it did not generate. The
// first control written for this test mutated a condition that does not exist
// in this file, so it changed nothing and passed -- an unapplied mutation and
// a surviving property look identical from the outside. The control that
// counts replaces the whole compound condition with `true`.
//
// NewAllFrom is included because it holds state of its own beyond Trip: once
// it refuses, it refuses everything after. A version that latched that state
// without ever being armed would fail every allocation from the first, and
// nothing else here would notice.
func TestAZeroPointsRefusesNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		make func(*fault.Points) *alloc.Fault
	}{
		{"New", func(p *fault.Points) *alloc.Fault { return alloc.New(p, alloc.Go()) }},
		{"NewAllFrom", func(p *fault.Points) *alloc.Fault { return alloc.NewAllFrom(p, alloc.Go()) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := c.make(&fault.Points{})

			// Several allocations, because NewAllFrom's refusal is sticky: one
			// success proves nothing about the second.
			for i := range 5 {
				b, err := a.Bytes(16)
				if err != nil {
					t.Fatalf("allocation %d refused with nothing armed: %v", i, err)
				}
				if len(b) != 16 {
					t.Errorf("allocation %d returned %d bytes, want 16", i, len(b))
				}
			}

			// Nothing was refused, so everything handed out is still
			// outstanding, and the count must say exactly that rather than
			// reporting a refusal that did not happen.
			if got := a.Outstanding(); got != 5 {
				t.Errorf("Outstanding() = %d after five allocations, want 5", got)
			}
		})
	}
}
