package fault

import "sync"

// Points counts the operations a scenario performs and reports which one must
// fail.
//
// A Points is safe for concurrent use. An adapter may perform operations from
// several goroutines, and the count must not race.
//
// A zero Points arms nothing, so every operation succeeds.
type Points struct {
	mu    sync.Mutex
	armed int  // the operation that must fail, counting from 1; 0 arms nothing
	seen  int  // operations performed so far
	fired bool // whether the armed operation has been reached
}

// hasFired reports whether the armed operation was reached.
//
// Unexported on purpose: Sweep is the only caller, and it lives in this
// package. An adapter never asks this question, so it never has to be public.
func (p *Points) hasFired() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fired
}

// Trip counts one operation and reports whether this is the operation that
// must fail.
//
// An adapter calls Trip once, immediately before each operation it performs.
// When Trip returns true, the adapter must skip the operation and return an
// error that its own domain would produce:
//
//	if p.Trip() {
//		return nil, &os.PathError{Op: "open", Path: name, Err: syscall.EIO}
//	}
//
// Trip returns true at most once for each Points. Every later call returns
// false, so one pass of a sweep fails one operation and no more. A zero Points
// arms nothing, and Trip always returns false for it.
//
// Trip is safe for concurrent use. That prevents a data race. It does not make
// a sweep meaningful when several goroutines share one Points: the N-th
// operation overall is a different operation on every run, because the
// scheduler picks the interleaving. See the package documentation.
func (p *Points) Trip() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Count before comparing. That ordering is what makes a zero Points inert
	// without a guard: its armed is 0, and seen can never be 0 again once it
	// has been incremented. Move this line below the comparison and a Points a
	// caller built by hand fails its first operation.
	p.seen++

	if p.armed == p.seen {
		// Record it as well as report it. Sweep reads this through hasFired to
		// decide whether the sequence has ended, so a Trip that reports true
		// without recording it makes the sweep stop one pass early — and a
		// sweep that stops early still reports a pass.
		p.fired = true
		return p.fired
	}

	return false
}
