package alloc_test

import (
	"reflect"
	"testing"

	"github.com/dd0wney/fault"
	faultalloc "github.com/dd0wney/fault/alloc"
)

// Every method of the wrapped interface is either counted or deliberately not,
// and a method in neither is a defect this test names.
//
// Contract rule 3 says an operation that skips Trip is invisible to the sweep,
// and that nothing detects it except a test that counts independently and
// compares. TestEveryAllocationCallsTrip does the counting. This does the
// comparing, against the interface itself rather than against a list somebody
// remembered to update.
//
// Measured on fault/fs on 2026-08-31: a method added to the interface and
// implemented without Trip passed the whole test suite, go vet, the coupling
// gate, and the mutation gate at a perfect score. The gate cannot express that
// defect, so it is asserted by hand here.
var allocCounted = map[string]bool{
	"Bytes": true,
}

// allocExempt carries the REASON as the value, because a bare list of names
// reads as an oversight and a reason reads as a decision. A reader who
// disagrees with the reason can argue with it; a reader looking at a name has
// nothing to argue with.
var allocExempt = map[string]string{
	"Free": "returns nothing, so it cannot report an injected failure to anybody. " +
		"An adapter that counted it would consume an operation index and inject an " +
		"error no caller could ever see, which is a pass that measured nothing.",
}

func TestEveryAllocatorMethodIsCountedOrExempt(t *testing.T) {
	iface := reflect.TypeOf((*faultalloc.Allocator)(nil)).Elem()

	if iface.NumMethod() == 0 {
		t.Fatal("reflection reported no methods on alloc.Allocator, so this check compared nothing")
	}

	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		_, counted := allocCounted[name]
		reason, exempt := allocExempt[name]

		switch {
		case counted && exempt:
			t.Errorf("%s is listed as counted AND as exempt (%q). It is one or the other", name, reason)
		case !counted && !exempt:
			t.Errorf("alloc.Allocator has %s and nothing here says whether it counts. "+
				"An operation that skips Trip is invisible to the sweep, and an exemption "+
				"nobody wrote down is indistinguishable from an oversight", name)
		}
	}

	for name := range allocCounted {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("%s is listed as counted and alloc.Allocator does not declare it", name)
		}
	}
	for name := range allocExempt {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("%s is listed as exempt and alloc.Allocator does not declare it", name)
		}
	}
}

// THE TIE THAT STOPS THE MAPS ABOVE BECOMING A SECOND LIST.
//
// The maps say Bytes counts and Free does not. This asserts it from the other
// end, by observation: a scenario performing one of each runs len(counted)+1
// passes, because the sweep ends on the pass that arms an operation the
// scenario never reaches.
//
// If Free started counting, this fails while the maps still claim otherwise,
// and the disagreement is the finding. A map that only ever agrees with itself
// is the shape this whole file exists to remove.
func TestTheCountedSetMatchesWhatASweepObserves(t *testing.T) {
	passes := 0
	for _, p := range fault.Sweep(t) {
		passes++
		a := faultalloc.New(p, faultalloc.Go())
		if b, err := a.Bytes(8); err == nil {
			a.Free(b)
		}
	}
	if want := len(allocCounted) + 1; passes != want {
		t.Errorf("a scenario performing one of every Allocator method ran %d passes, want %d "+
			"(%d counted operation(s), plus the pass that proves the sequence ended). "+
			"Either a method started counting or one stopped", passes, want, len(allocCounted))
	}
}
