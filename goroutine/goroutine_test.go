package goroutine_test

import (
	"strings"
	"testing"
	"time"

	"github.com/dd0wney/fault/goroutine"
)

// A snapshot taken and immediately compared reports nothing new.
func TestSinceReportsNothingWhenNothingStarted(t *testing.T) {
	before := goroutine.Take()
	if got := before.Since(0); len(got) != 0 {
		t.Errorf("Since = %v, want empty", got)
	}
}

// THE PROPERTY THE WHOLE PACKAGE EXISTS FOR. A goroutine still running is
// reported, and it is named well enough to find.
func TestSinceReportsAGoroutineThatIsStillRunning(t *testing.T) {
	before := goroutine.Take()

	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		<-stop
	}()
	<-started
	defer close(stop)

	got := before.Since(0)
	if len(got) != 1 {
		t.Fatalf("Since = %v, want exactly one goroutine", got)
	}
	if !strings.Contains(got[0], "goroutine_test") {
		t.Errorf("Since = %q, which does not name the function that started it", got[0])
	}
}

// A goroutine that has ENDED is not a leak. Reporting it would make every
// scenario that starts a worker and stops it look broken.
func TestSinceIgnoresAGoroutineThatEnded(t *testing.T) {
	before := goroutine.Take()

	done := make(chan struct{})
	go func() { close(done) }()
	<-done

	if got := before.Since(time.Second); len(got) != 0 {
		t.Errorf("Since = %v, want empty -- the goroutine had already ended", got)
	}
}

// THE SETTLE IS NOT COSMETIC. A worker told to stop needs a moment to go, and
// a check with no patience reports every clean shutdown as a leak.
func TestSinceWaitsForAGoroutineOnItsWayOut(t *testing.T) {
	before := goroutine.Take()

	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		<-stop
	}()
	<-started

	// CONTROL -- with no patience it IS reported, so the settle below is the
	// thing being tested and not an accident of timing.
	if got := before.Since(0); len(got) != 1 {
		t.Fatalf("with no settle, Since = %v, want one -- this control must hold "+
			"or the settle test proves nothing", got)
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(stop)
	}()

	if got := before.Since(5 * time.Second); len(got) != 0 {
		t.Errorf("Since = %v, want empty -- the goroutine ended within the settle", got)
	}
}

// Goroutines that existed BEFORE the snapshot are never reported. Without
// this the count is absolute rather than a delta, and every point of a sweep
// inherits the leaks of the points before it.
func TestSinceIgnoresGoroutinesThatPredateTheSnapshot(t *testing.T) {
	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		<-stop
	}()
	<-started
	defer close(stop)

	before := goroutine.Take()

	if got := before.Since(0); len(got) != 0 {
		t.Errorf("Since = %v, want empty -- that goroutine predates the snapshot", got)
	}
}

// Two goroutines from one function are two entries. A report keyed by name
// would fold them together and understate every reading.
func TestSinceCountsEachGoroutineSeparately(t *testing.T) {
	before := goroutine.Take()

	stop := make(chan struct{})
	started := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			started <- struct{}{}
			<-stop
		}()
	}
	<-started
	<-started
	defer close(stop)

	if got := before.Since(0); len(got) != 2 {
		t.Errorf("Since = %v, want two entries for two goroutines", got)
	}
}

// Since must not WAIT when the answer is already known.
//
// The settle exists for a goroutine on its way out. With nothing new running
// there is nothing to wait for, and a loop that sleeps to the deadline anyway
// turns every clean pass of a sweep into a full settle. Over the 30 points of
// a real sweep that is the difference between a second and a minute.
func TestSinceReturnsAtOnceWhenNothingIsRunning(t *testing.T) {
	before := goroutine.Take()

	start := time.Now()
	got := before.Since(30 * time.Second)
	elapsed := time.Since(start)

	if len(got) != 0 {
		t.Fatalf("Since = %v, want empty", got)
	}
	if elapsed > 5*time.Second {
		t.Errorf("Since waited %v for a settle it did not need, against a deadline "+
			"of 30s -- it must return as soon as the set is empty", elapsed)
	}
}
