package fault_test

import (
	"sync"
	"testing"

	"github.com/dd0wney/fault"
)

// A Points a caller builds by hand arms nothing, so it must never fail an
// operation. What makes that true is the ordering inside Trip: seen is
// incremented before the comparison, so it is at least 1 and can never equal an
// armed of 0. Move that increment below the comparison and this test goes red.
func TestZeroPointsNeverTrips(t *testing.T) {
	var p fault.Points
	for i := 1; i <= 5; i++ {
		if p.Trip() {
			t.Fatalf("the zero Points tripped on call %d", i)
		}
	}
	if fault.Fired(&p) {
		t.Error("the zero Points reported that it fired")
	}
}

// Trip must return true on the armed call, counting from 1, and on no other.
func TestTripFiresOnTheArmedCall(t *testing.T) {
	for _, armed := range []int{1, 2, 3, 7} {
		p := fault.Armed(armed)

		var got []int
		for call := 1; call <= 10; call++ {
			if p.Trip() {
				got = append(got, call)
			}
		}

		if len(got) != 1 || got[0] != armed {
			t.Errorf("armed at %d: tripped on calls %v, want exactly [%d]", armed, got, armed)
		}
		if !fault.Fired(p) {
			t.Errorf("armed at %d: Fired is false after the armed call ran", armed)
		}
	}
}

// A pass that never reaches the armed operation must report that it did not
// fire. Sweep reads this to decide that the sequence has ended, so a wrong
// answer here makes every sweep stop one pass early and still report a pass.
func TestTripDoesNotFireBeyondTheSequence(t *testing.T) {
	p := fault.Armed(4)
	for call := 1; call <= 3; call++ {
		if p.Trip() {
			t.Fatalf("tripped on call %d, but the armed call is 4", call)
		}
	}
	if fault.Fired(p) {
		t.Error("Fired is true, but the armed call never ran")
	}
}

// Exactly one of many concurrent callers may trip. Run with -race.
func TestTripIsSafeForConcurrentUse(t *testing.T) {
	const callers = 64
	p := fault.Armed(callers / 2)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		hits int
	)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p.Trip() {
				mu.Lock()
				hits++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if hits != 1 {
		t.Errorf("%d callers tripped, want exactly 1", hits)
	}
}
