package fs_test

import (
	"reflect"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

// Contract rule 3, one level up from TestEveryOperationCallsTrip.
//
// That test drives a table and proves every operation IN THE TABLE calls Trip.
// It says nothing about a method the table omits, and the table is written by
// hand. This one compares the table against the interface itself.
//
// MEASURED 2026-08-31, and this is why it exists. A seventh method added to
// fs.FS, implemented on Fault without calling Trip, passed every gate this
// repository has: 242 tests, go vet, the coupling gate, and the mutation gate
// at 1.000000 against a floor of 1.00. The mutation gate cannot express the
// defect, because `return f.base.Chmod(name, mode)` has no mutant meaning
// "this should have called Trip".
//
// The compiler is a real gate and it stops exactly one thing. Adding a method
// to fs.FS breaks crash's remapFS, crash's Recorder and five test stubs, so a
// method cannot be added without every implementation being written. Nothing
// makes any of those implementations COUNT.
//
// The core's own documentation predicts all of this: "Nothing detects this
// except a test that counts operations independently and compares." The
// counting exists. The comparison did not.
//
// It reads the SAME table the behavioural tests drive. A second list would
// drift from the first, and the drift is the defect being closed.
func TestTheOperationTableCoversTheFSInterface(t *testing.T) {
	iface := reflect.TypeOf((*faultfs.FS)(nil)).Elem()

	// The interface is the authority: every method must be exercised.
	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		if _, ok := one[name]; !ok {
			t.Errorf("fs.FS has %s and the operation table does not, so no test proves it calls Trip. "+
				"An operation that skips the call is invisible to the sweep, and the sweep then reports "+
				"a clean walk over a sequence it never visited.", name)
		}
	}

	// And the reverse: a table entry for a method that no longer exists is a
	// test asserting something about nothing. It cannot happen while the entry
	// still compiles against the interface, so this guard is here for the day
	// the table stops being typed against fs.FS.
	for name := range one {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("the operation table has %s and fs.FS does not, so that entry tests a method "+
				"the interface no longer declares", name)
		}
	}

	// The positive control. If the interface reported no methods, both loops
	// above would pass having compared nothing.
	if iface.NumMethod() == 0 {
		t.Fatal("reflection reported no methods on fs.FS, so this check compared nothing")
	}
}

// The same comparison one level down, over fs.File.
//
// THE OPTIONAL METHODS ARE OUTSIDE THIS CHECK, and saying so is part of it.
// Seek and WriteAt are not members of fs.File — a caller type-asserts for them,
// exactly as it does for io.ReaderFrom — so reflection over the interface
// cannot see them and their table in positional_test.go stays hand-maintained.
//
// That is the same defect this file closes, one level further out, and it is
// left open deliberately rather than hidden: an optional method set has no
// declaration to compare against. The alternative would be a second
// hand-written list of the optional interfaces, which is the thing being
// removed rather than a fix for it.
func TestTheFileTableCoversTheFileInterface(t *testing.T) {
	iface := reflect.TypeOf((*faultfs.File)(nil)).Elem()

	inTable := make(map[string]bool, len(fileOps))
	for _, o := range fileOps {
		inTable[o.name] = true
	}

	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		if !inTable[name] {
			t.Errorf("fs.File has %s and the file operation table does not, so no test proves it "+
				"returns the injected error", name)
		}
	}

	for name := range inTable {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("the file operation table has %s and fs.File does not", name)
		}
	}

	if iface.NumMethod() == 0 {
		t.Fatal("reflection reported no methods on fs.File, so this check compared nothing")
	}
}
