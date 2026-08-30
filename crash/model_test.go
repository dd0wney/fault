package crash

import "testing"

// The zero Model must be the STRICT one, not the empty one. A caller who
// writes crash.Model{} gets whole-call loss, the POSIX metadata rule, and an
// exhaustive walk. This mirrors the core's zero Points, which arms nothing
// because counting happens before comparing.
func TestTheZeroModelIsTheStrictOne(t *testing.T) {
	var m Model

	if m.Sector != 0 {
		t.Errorf("Sector = %d, want 0 so a whole Write call is the unit", m.Sector)
	}
	if m.SplitAtCrashOnly {
		t.Error("SplitAtCrashOnly = true, want false so every pending write may split")
	}
	if m.MetadataDurable {
		t.Error("MetadataDurable = true, want false so the POSIX rule is the default")
	}
	if m.Cover != Exhaustive {
		t.Errorf("Cover = %v, want Exhaustive as the zero value", m.Cover)
	}
}

func TestCoverValuesAreDistinct(t *testing.T) {
	if Exhaustive == Prefixes {
		t.Fatal("Exhaustive and Prefixes are the same value")
	}
}
