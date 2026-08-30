package crash

import "sort"

// Observed reports every path the recorder served, relative to the record root,
// slash-separated, sorted and without duplicates. A rename contributes both of
// its names.
//
// It answers one question, and it is a question no other guard here asks: did
// the scenario reach the seam for the file you care about?
//
//	if !slices.Contains(rec.Observed(), "index.lsa") {
//		t.Fatal("the recorder never saw the LSA snapshot, so this sweep says nothing about it")
//	}
//
// The precedent is [github.com/dd0wney/fault/fs.Outstanding]: expose the fact
// and let the caller assert. This package must not decide what "reached" means,
// because only the caller knows which file carries the durability it is testing.
//
// # What the other guards already cover
//
// The TOTAL bypass is refused. A scenario that changed nothing through the seam
// has no crash point, and the plan fails with errNoMutations.
//
// A bypass that adds or alters a NAME under the record root is refused too, and
// loudly. The positive control replays the whole record and compares it with
// the directory the scenario wrote, so a file created around the seam appears
// in one and not the other. Measured 2026-08-31, and
// TestTheReplayControlAlreadyCatchesABypassThatAddsAName holds it there.
//
// # What is left, and why this method exists
//
// A bypass that leaves the recorded tree identical is silent under both. Three
// shapes do, all measured:
//
//   - The bypass writes OUTSIDE the record root. The control reads that root
//     alone, so it never looks. This is the ordinary shape, and the live case:
//     graphdb's SaveToFile calls os.OpenFile and os.Rename directly.
//   - The bypass creates and removes a name under the root. It is gone before
//     the control looks.
//   - The bypass rewrites a recorded path with the bytes already there.
//
// In each of those, the record is not empty, the tree matches, states are
// built, the check runs, and nothing says the path was never seen. errNoMutations
// asks whether anything happened. This asks whether the thing you named happened.
func (r *Recorder) Observed() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	seen := make(map[string]struct{}, len(r.entries))
	for _, e := range r.entries {
		// The empty path belongs to an entry the recorder refused, because the
		// name lay outside the root. The refusal already fails the run, and a
		// caller reading this list must not be handed a path that names nothing.
		if e.path != "" {
			seen[e.path] = struct{}{}
		}
		// A rename serves two names. A store whose only operation on its
		// destination is the rename would otherwise be reported as unseen at
		// exactly the name the caller asks about.
		if e.to != "" {
			seen[e.to] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	// Sorted because map iteration is randomised and a caller that prints this
	// list on failure needs the same order on every run.
	sort.Strings(out)
	return out
}
