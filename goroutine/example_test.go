package goroutine_test

import (
	"fmt"
	"time"

	"github.com/dd0wney/fault/goroutine"
)

// Since reports what started after the snapshot and is still running, so a
// worker stopped before the check is not counted as a leak.
func ExampleSnapshot_Since() {
	before := goroutine.Take()

	// A worker that is stopped properly.
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-stopped
	}()

	fmt.Println("while running:", len(before.Since(0)))

	close(stopped)
	<-done

	// The settle gives a worker on its way out time to go. Without it, every
	// clean shutdown reads as a leak.
	fmt.Println("after stopping:", len(before.Since(2*time.Second)))

	// Output:
	// while running: 1
	// after stopping: 0
}
