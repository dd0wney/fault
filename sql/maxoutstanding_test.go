package sql_test

import (
	"context"
	"testing"

	"github.com/dd0wney/fault"
	faultsql "github.com/dd0wney/fault/sql"
)

// The reading this method exists for, stated as the discrimination itself.
//
// Outstanding() == 0 is the pass condition for "nothing leaked". It is also
// exactly what a scenario that never connected returns. The two are
// indistinguishable by Outstanding alone, and MaxOutstanding is the only thing
// that separates them.
//
// A scenario that took a wrong turn before its first Connect -- a config error,
// a guard that returned early, a -run pattern that reached the wrong test -- is
// the case that looks like success everywhere else in a suite.
func TestOnlyMaxOutstandingSeparatesCleanFromNeverConnected(t *testing.T) {
	neverConnected := faultsql.New(&fault.Points{}, &testDriver{})

	connectedAndClosed := faultsql.New(&fault.Points{}, &testDriver{})
	c, err := connectedAndClosed.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if neverConnected.Outstanding() != connectedAndClosed.Outstanding() {
		t.Fatalf("Outstanding separates the two scenarios (%d and %d), so this test is "+
			"measuring something other than the case the method exists for",
			neverConnected.Outstanding(), connectedAndClosed.Outstanding())
	}

	if got := neverConnected.MaxOutstanding(); got != 0 {
		t.Errorf("no connection was opened, MaxOutstanding = %d, want 0", got)
	}
	if got := connectedAndClosed.MaxOutstanding(); got != 1 {
		t.Errorf("one connection was opened and closed, MaxOutstanding = %d, want 1", got)
	}
}

// The mark does not fall when the connection closes.
func TestMaxOutstandingHoldsAfterTheConnectionCloses(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &testDriver{})

	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got := f.MaxOutstanding(); got != 1 {
		t.Fatalf("MaxOutstanding = %d while holding a connection, want 1", got)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if got := f.Outstanding(); got != 0 {
		t.Errorf("Outstanding = %d after close, want 0", got)
	}
	if got := f.MaxOutstanding(); got != 1 {
		t.Errorf("MaxOutstanding = %d after close, want 1 — a high-water mark that falls "+
			"answers the same question Outstanding already answers", got)
	}
}

// A refused Connect hands nothing out, so it must not move the mark.
//
// This is the rule Outstanding already follows, and it is the one that matters
// most for this reading: a mark that counted refusals would answer "yes, a
// connection was live" for a scenario in which every Connect failed, which is
// precisely the scenario the method exists to expose.
func TestARefusedConnectDoesNotMoveTheMark(t *testing.T) {
	var refusedAtLeastOnce bool
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{})

		c, err := f.Connect(context.Background())
		if err != nil {
			refusedAtLeastOnce = true
			if got := f.MaxOutstanding(); got != 0 {
				t.Errorf("point %d: the connect was refused, MaxOutstanding = %d, want 0", n, got)
			}
			continue
		}
		if got := f.MaxOutstanding(); got != 1 {
			t.Errorf("point %d: a connection was handed out, MaxOutstanding = %d, want 1", n, got)
		}
		_ = c.Close()
	}

	// The positive control. If no pass ever refused, the assertion above ran
	// over nothing.
	if !refusedAtLeastOnce {
		t.Fatal("no pass refused a connect, so the refusal case was never measured")
	}
}
