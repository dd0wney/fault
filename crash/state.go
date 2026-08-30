package crash

import "path"

// crashPoints returns the entry indexes after which a crash point sits.
//
// A read, a sync and a plain open change nothing, so a crash after one of
// them leaves the same state as a crash after the previous mutation.
// Visiting both would double the walk and prove nothing new.
func crashPoints(entries []entry) []int {
	var out []int
	for _, e := range entries {
		if e.k.mutates() {
			out = append(out, e.n)
		}
	}
	return out
}

// split partitions the mutating entries at or before k into those a crash at
// k would keep on disk (durable) and those it may lose (pending).
//
// A write or a truncate is covered by a sync on its own file handle. A
// create, rename, remove or mkdir is covered by a sync on a handle opened on
// the directory that holds the name -- a file sync does not reach it, which
// is the missing-parent-fsync defect this package exists to catch. A rename
// holds two names, so both directories must be covered. Close covers
// nothing, because POSIX does not make close(2) flush.
//
// k bounds what has happened: an entry or a sync with an index after k has
// not run yet, so it cannot make anything durable. The walk goes from k down
// to the start, recording every sync it passes, so by the time it reaches a
// mutating entry it already knows every sync that comes after that entry and
// at or before k.
func split(entries []entry, k int, m Model) (durable, pending []int) {
	fileSynced := map[string]bool{}
	dirSynced := map[string]bool{}
	isDurable := map[int]bool{}

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.n > k {
			continue
		}
		switch {
		case e.k == kSync && e.dir:
			dirSynced[e.path] = true
			continue
		case e.k == kSync:
			fileSynced[e.path] = true
			continue
		case !e.k.mutates():
			continue
		}

		switch e.k {
		case kWrite, kTruncate:
			isDurable[e.n] = fileSynced[e.path]
		default:
			// Metadata. path.Dir("data.tmp") returning "." matches a sync
			// recorded against the record root itself, which the recorder
			// spells "." too -- see Recorder.rel.
			isDurable[e.n] = m.MetadataDurable ||
				(dirSynced[path.Dir(e.path)] && (e.to == "" || dirSynced[path.Dir(e.to)]))
		}
	}

	for _, e := range entries {
		if e.n > k || !e.k.mutates() {
			continue
		}
		if isDurable[e.n] {
			durable = append(durable, e.n)
			continue
		}
		pending = append(pending, e.n)
	}
	return durable, pending
}
