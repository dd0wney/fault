package fault

// This file compiles only under `go test`. It re-exports unexported entry
// points so that the external test package can reach them, which is the
// standard library's own mechanism for this. It ships nothing, and it wraps the
// production code rather than substituting a second implementation, so the
// tested artifact stays the shipped artifact.

// Armed returns a Points armed at operation n.
func Armed(n int) *Points { return &Points{armed: n} }

// Fired reports whether p reached its armed operation.
func Fired(p *Points) bool { return p.hasFired() }

// Walk is the sweep loop without the test-failure wrapper. It returns a
// diagnostic, so a test can assert on the two ways a sweep proves nothing.
func Walk(yield func(int, *Points) bool) error { return walk(yield) }

// MaxOps is the bound at which a walk gives up.
const MaxOps = maxOps

// The diagnostics Walk returns.
var (
	ErrNoOperations  = errNoOperations
	ErrNoTermination = errNoTermination
)
