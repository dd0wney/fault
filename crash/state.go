package crash

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
)

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

// units splits the pending entries into the things a crash can lose.
//
// A metadata entry never splits, because no filesystem half-renames a file.
// A write splits at sector boundaries of its ABSOLUTE offset range, so two
// writes into the same sector of the same file split at the same place.
// Model.Sector == 0 keeps a whole Write call as the unit. SplitAtCrashOnly
// limits splitting to the write at k, so every earlier pending write is lost
// whole -- this is what SQLite's crash simulation does, and it buys the
// part-written record without paying for it at every earlier write.
func units(entries []entry, pending []int, k int, m Model) []unit {
	byIndex := make(map[int]entry, len(entries))
	for _, e := range entries {
		byIndex[e.n] = e
	}

	var out []unit
	for _, n := range pending {
		e := byIndex[n]
		splittable := e.k == kWrite && m.Sector > 0 && len(e.data) > 0
		if splittable && m.SplitAtCrashOnly && n != k {
			splittable = false
		}
		if !splittable {
			out = append(out, unit{entry: n})
			continue
		}
		out = append(out, sectorsOf(e, int64(m.Sector))...)
	}
	return out
}

// sectorsOf cuts one write at the sector boundaries its absolute offset
// falls on. from and to come out relative to the write's own data, because
// replay slices e.data[r.from:r.to] and knows nothing of the file offset.
//
// sect is the loop counter i, carried rather than computed from from and to:
// the last sector of a write is usually short, so from/(to-from) names it
// wrongly. See units_test.go for the case this guards.
//
// A write that never crosses a boundary comes back as ONE WHOLE unit rather
// than as sector 0 of one, because no split happened. Naming it ".s0" would
// claim a granularity that did not apply, and it would give two models that
// produce identical states two different names: Model{Sector: 0} would address
// a state as "lost=a:write1" while a sector larger than every write addressed
// the same state as "lost=a:write1.s0". A state name is a go test -run
// address, so that is a real difference and not a cosmetic one.
func sectorsOf(e entry, sector int64) []unit {
	start := e.off
	end := e.off + int64(len(e.data))

	var out []unit
	for at, i := start, 0; at < end; i++ {
		next := (at/sector + 1) * sector
		if next > end {
			next = end
		}
		out = append(out, unit{entry: e.n, from: at - start, to: next - start, sect: i})
		at = next
	}
	if len(out) == 1 {
		return []unit{{entry: e.n}}
	}
	return out
}

// closure drops every entry that needs a dropped one, repeatedly, until
// nothing more falls.
//
// Without it the generator produces states that no power cut can make -- a
// file holding data with no directory entry, most often. Those are not
// findings. They train a reader to ignore the tool, which is worse than
// having no tool.
//
// The loop keeps going until a pass changes nothing, rather than trusting a
// single pass, because entries is not guaranteed to arrive in dependency
// order -- only in dependency order does one forward pass already cascade a
// whole chain.
func closure(entries []entry, keep map[int]bool) map[int]bool {
	out := make(map[int]bool, len(keep))
	for k, v := range keep {
		out[k] = v
	}
	for changed := true; changed; {
		changed = false
		for _, e := range entries {
			if !out[e.n] {
				continue
			}
			for _, need := range e.needs {
				if !out[need] {
					out[e.n] = false
					changed = true
					break
				}
			}
		}
	}
	return out
}

// maxStates bounds one crash point's exhaustive walk.
//
// MEASURED 2026-08-30: over the four reference stores under Model{Sector: 4096}
// and both metadata rulings, the largest pending-unit count at any crash point
// is 4, which is 16 states. noFileSync and noDirSync under the POSIX rule both
// reach it. 4096 is 256 times that, and it admits 12 pending units where the
// measurement needed 4.
const maxStates = 4096

var errTooManyStates = errors.New("crash: too many states for an exhaustive cover")

// subsets returns the states one crash point can produce, by the rule c.
// Each result names the units that were LOST, so the empty result is the
// state where everything pending reached the disk.
func subsets(u []unit, c Cover) ([][]unit, error) {
	if c == Prefixes {
		// The pending units persist in log order, so what a crash loses is a
		// suffix of that order: the earliest unit survives longest, and the
		// latest unit is lost first. i counting down from len(u) to 0 walks
		// exactly those u+1 suffixes, from "lose nothing" to "lose
		// everything".
		out := make([][]unit, 0, len(u)+1)
		for i := len(u); i >= 0; i-- {
			out = append(out, append([]unit(nil), u[i:]...))
		}
		return out, nil
	}

	n := len(u)
	// n >= 63 is refused outright: int64(1)<<63 goes negative, so a shift
	// that wide no longer means "2 to the power of n" and the comparison
	// below could not be trusted.
	if n >= 63 {
		return nil, tooManyStates(n)
	}
	total64 := int64(1) << uint(n)
	if total64 > maxStates {
		return nil, tooManyStates(n)
	}
	total := int(total64)

	out := make([][]unit, 0, total)
	for mask := 0; mask < total; mask++ {
		var lost []unit
		for i, x := range u {
			if mask&(1<<uint(i)) != 0 {
				lost = append(lost, x)
			}
		}
		out = append(out, lost)
	}
	return out, nil
}

// tooManyStates names the unit count, the resulting state count, and
// Prefixes as the way forward. subsets returns this instead of visiting a
// truncated subset -- a partial walk reports a pass exactly the same way a
// complete one does, which is the failure this package exists to catch.
func tooManyStates(n int) error {
	return fmt.Errorf("%w: %d pending units give %s states, and the cap is %d. Use Prefixes instead",
		errTooManyStates, n, countOf(n), maxStates)
}

// countOf renders 2^n as a state count. It reports the exact number where an
// int64 can hold it, and switches to the "2^n" form before that stops being
// true, so the message always states the real size rather than a wrapped
// (or negative) one.
func countOf(n int) string {
	if n >= 63 {
		return fmt.Sprintf("2^%d", n)
	}
	return fmt.Sprintf("%d", int64(1)<<uint(n))
}

// fingerprint is a stable hash of a tree, used to drop duplicate states
// before any of them becomes a subtest.
//
// The keys come from t.keys(), which sorts. Go randomises map iteration on
// purpose, so a fingerprint that ranged over t directly would merge
// different states on one run and split identical ones on the next, and
// go test -run could never reliably reproduce a failure.
func fingerprint(t tree) string {
	h := sha256.New()
	for _, k := range t.keys() {
		n := t[k]
		fmt.Fprintf(h, "%s\x00%v\x00%d\x00", k, n.dir, len(n.data))
		h.Write(n.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
