package role

import "fmt"

// Role names the actor that performs an operation.
type Role string

// Unknown is the role of an operation an adapter cannot classify.
//
// It is a real role with its own counter, and that matters. If an unclassified
// operation joined the role under test, it would shift every later ordinal and
// the sweep would silently walk a different sequence. If it were dropped, the
// same thing happens and the trace stops matching the run. Its own counter does
// neither.
const Unknown Role = "unknown"

// Key names a failure point structurally: which actor, which operation, and
// which occurrence of that operation within that actor.
//
// An ordinal is what a sweep walks by, because that is the only way to reach
// every operation. A Key is what it reports, because an ordinal means nothing
// outside the exact scenario that produced it. "The flush worker's second sync"
// survives an extra write before it. "The flush worker's 7th operation" does
// not.
type Key struct {
	Role Role
	Op   string // the adapter's own name for the operation
	Nth  int    // which occurrence of that Op within that Role, from 1
}

func (k Key) String() string { return fmt.Sprintf("%s/%s#%d", k.Role, k.Op, k.Nth) }
