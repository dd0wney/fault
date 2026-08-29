package thin

import "testing"

// Deliberately weak: it runs the code and asserts nothing.
func TestWeak(t *testing.T) { _ = Big(5) }
