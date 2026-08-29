package alloc

import (
	"sync"

	"github.com/dd0wney/fault"
)

// Fault is an Allocator that refuses one allocation, or every allocation from
// one onward, chosen by a [fault.Points].
//
// New returns this concrete type rather than the Allocator interface, because
// Outstanding is not part of Allocator and a caller needs it.
type Fault struct {
	p    *fault.Points
	base Allocator

	// allFrom selects SQLite's second loop: once a refusal happens, every
	// later allocation is refused too.
	allFrom bool

	mu          sync.Mutex
	refusing    bool // allFrom only: a refusal has already happened
	outstanding int  // buffers handed out and not yet freed
}

// New refuses one allocation and then behaves normally. It finds handlers that
// cope with a single failure and continue.
func New(p *fault.Points, base Allocator) *Fault {
	return &Fault{p: p, base: base}
}

// NewAllFrom refuses one allocation and every allocation after it. It finds
// handlers that only work when a later allocation succeeds -- a cleanup path
// that allocates in order to clean up.
//
// The core knows nothing about this mode. It supplies one failure point, and
// this adapter remembers that the point fired.
func NewAllFrom(p *fault.Points, base Allocator) *Fault {
	return &Fault{p: p, base: base, allFrom: true}
}

// Bytes returns a buffer of n bytes, or refuses.
func (a *Fault) Bytes(n int) ([]byte, error) {
	// Rule 1: every allocation the code under test performs is an operation
	// the sweep must see, including one that allFrom mode is going to refuse
	// on its own. Skip this call and the sweep undercounts the sequence.
	tripped := a.p.Trip()

	// TODO(ddowney): decide whether to refuse.
	//
	// Two reasons to refuse, and only one of them is Trip:
	//
	//  1. Trip fired. Always refuse, in either mode.
	//  2. This is allFrom mode and a refusal has already happened. Refuse too,
	//     without consulting Trip -- that call was already made above.
	//
	// Record the first case when the mode is allFrom, or the second case can
	// never become true and NewAllFrom does nothing at all. a.refusing is the
	// only place that knowledge can live: the core has no notion of this mode.
	a.mu.Lock()
	if tripped && a.allFrom {
		a.refusing = true
	}
	refuse := tripped || a.refusing
	a.mu.Unlock()

	if refuse {
		return nil, ErrNoMemory
	}

	b, err := a.base.Bytes(n)
	if err != nil {
		return nil, err
	}

	// Count only what was actually handed out. A refused allocation and a
	// base allocator that failed on its own both returned above.
	a.mu.Lock()
	a.outstanding++
	a.mu.Unlock()
	return b, nil
}

// Free records that a buffer is no longer in use.
//
// A Free with no matching Bytes drives Outstanding negative, and that is
// deliberate. Two real scenarios produce one: a deferred Free placed before the
// error check, which runs on the very pass where the allocation was refused,
// and an ordinary double free. Both are defects in the code under test, and a
// negative count is the only thing that reports them.
//
// Clamping at zero would turn that finding into a plausible zero, and a
// plausible zero is what a leak detector must never produce. Panicking would be
// louder still, and would turn a defect in a scenario into a crash halfway
// through a sweep. TestAnUnmatchedFreeDrivesTheCountNegative is the gate.
//
// The real Free happens outside the lock: never hold your own mutex across a
// call into someone else's code.
func (a *Fault) Free(b []byte) {
	a.base.Free(b)

	a.mu.Lock()
	a.outstanding--
	a.mu.Unlock()
}

// Outstanding reports how many buffers have been handed out and not freed.
//
// This is the second of the three assertions a fault-injection loop needs: the
// operation failed, nothing leaked, the state is still valid. Drop it and every
// unwind path in the code under test can leak while the sweep still reports a
// clean walk.
func (a *Fault) Outstanding() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.outstanding
}
