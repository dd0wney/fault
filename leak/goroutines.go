package leak

import (
	"testing"
	"time"
)

// Delta is what goroutine.Snapshot answers.
type Delta interface {
	Since(settle time.Duration) []string
}

// Goroutines fails the test for every goroutine that started after snap
// was taken and is still running after settle. There is no "was the
// question live" reading for goroutines, because a Snapshot does not count
// starts, and the doc says so rather than pretending.
func Goroutines(t testing.TB, snap Delta, settle time.Duration) {
	t.Helper()
	// TODO(leak): not yet implemented.
}
