package role

import "sync"

// Points counts each actor's operations separately and reports which one must
// fail.
//
// A Points is safe for concurrent use, which is the whole reason this package
// exists: several actors call [Points.Op] at once, and each keeps its own count.
//
// A Points belongs to exactly one pass of a [Sweep].
type Points struct {
	target Role // the role under test; every other role runs untouched
	armed  int  // the target's operation that must fail, counting from 1

	mu     sync.Mutex
	seen   map[Role]int            // operations performed, per role
	nth    map[Role]map[string]int // occurrences of each Op, per role
	trace  []string                // the target's operations, in order
	fired  bool
	firedK Key
}

func newPoints(target Role, armed int) *Points {
	return &Points{
		target: target,
		armed:  armed,
		seen:   make(map[Role]int),
		nth:    make(map[Role]map[string]int),
	}
}

// Op counts one operation by one actor and reports whether this is the
// operation that must fail.
//
// An adapter calls it once, immediately before each operation, exactly as it
// would call [github.com/dd0wney/fault.Points.Trip]. The op string names the
// operation in the adapter's own vocabulary, and it must be a function of what
// the actor asked for and of nothing the environment supplied -- see the
// package documentation.
//
// Only the target role can be made to fail. Every other role is counted, so its
// operations appear in no trace and shift no ordinal, and then permitted.
func (p *Points) Op(r Role, op string) bool {
	if r == "" {
		r = Unknown
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.seen[r]++
	if p.nth[r] == nil {
		p.nth[r] = make(map[string]int)
	}
	p.nth[r][op]++

	if r != p.target {
		return false
	}
	p.trace = append(p.trace, op)

	if p.armed == 0 || p.seen[r] != p.armed {
		return false
	}
	p.fired = true
	p.firedK = Key{Role: r, Op: op, Nth: p.nth[r][op]}
	return true
}

// hasFired reports whether the target reached its armed operation.
func (p *Points) hasFired() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fired
}

// firedKey returns the structural key of the operation that failed.
func (p *Points) firedKey() (Key, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.firedK, p.fired
}

// traceOf returns a copy of the target's operations, in order.
func (p *Points) traceOf() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.trace))
	copy(out, p.trace)
	return out
}
