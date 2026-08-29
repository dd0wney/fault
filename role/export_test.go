package role

// This file compiles only under `go test`. It re-exports unexported entry
// points so the external test package can reach the diagnostics, which a test
// cannot observe through t.Fatal.

import "testing"

// Walk is the sweep loop without the test-failure wrapper.
func Walk(t *testing.T, target Role, yield func(int, *Points) bool) error {
	return walk(t, target, yield)
}

// MaxOps is the bound at which a walk gives up.
const MaxOps = maxOps

var (
	ErrNoOperations  = errNoOperations
	ErrNoTermination = errNoTermination
	ErrUnstable      = errUnstable
)
