package crash

import "fmt"

// unit is a byte range that a crash point can act on. entry names which
// write it belongs to, from and to bound the range within that write's own
// data (not the file offset), and sect names the sector it falls in when a
// Model splits by sector.
type unit struct {
	entry    int
	from, to int64
	sect     int
}

// whole reports whether u covers its entry entirely -- either a metadata
// entry, which never splits, or a write that Model chose not to split.
func (u unit) whole() bool { return u.from == 0 && u.to == 0 }

// replay applies the entries named by present, in log order, to a copy of
// snap, and returns the resulting tree.
//
// An entry absent from present did not reach the disk. The caller has
// already applied the dependency closure, so a present write should never
// name a path whose create is absent -- but replay still refuses that
// pairing rather than inventing a file, because a silent invention here
// would make the generator's defect look like the store's, and every state
// built after it would be a fiction.
//
// A byte range named in lost is NOT zeroed. It keeps whatever the replay
// already had there, which is the truthful model: the prior content is
// already in the record, as an earlier entry. See rebuild_test.go for the
// graphdb counter-example this rule exists to cover.
func replay(snap tree, entries []entry, present map[int]bool, lost map[int][]unit) (tree, error) {
	out := snap.clone()
	for _, e := range entries {
		if !present[e.n] || !e.k.mutates() {
			continue
		}
		switch e.k {
		case kCreate:
			if _, ok := out[e.path]; !ok {
				out[e.path] = node{}
			}
		case kMkdir:
			out[e.path] = node{dir: true}
		case kWrite:
			n, ok := out[e.path]
			if !ok {
				return nil, fmt.Errorf("crash: entry %d writes to %q, which no present entry created", e.n, e.path)
			}
			for _, r := range presentRanges(int64(len(e.data)), lost[e.n]) {
				n.data = writeAt(n.data, e.off+r.from, e.data[r.from:r.to])
			}
			out[e.path] = n
		case kTruncate:
			n, ok := out[e.path]
			if !ok {
				return nil, fmt.Errorf("crash: entry %d truncates %q, which no present entry created", e.n, e.path)
			}
			n.data = resize(n.data, e.size)
			out[e.path] = n
		case kRename:
			n, ok := out[e.path]
			if !ok {
				return nil, fmt.Errorf("crash: entry %d renames %q, which no present entry created", e.n, e.path)
			}
			delete(out, e.path)
			out[e.to] = n
		case kRemove:
			delete(out, e.path)
		}
	}
	return out, nil
}

// presentRanges returns the complement of the lost ranges within [0, size),
// in order. It turns "these byte ranges did not land" into "these ranges
// did", which is the only form replay can apply.
func presentRanges(size int64, lost []unit) []unit {
	if len(lost) == 0 {
		return []unit{{from: 0, to: size}}
	}
	gone := make([]bool, size)
	for _, u := range lost {
		for i := u.from; i < u.to && i < size; i++ {
			gone[i] = true
		}
	}
	var out []unit
	for i := int64(0); i < size; i++ {
		if gone[i] {
			continue
		}
		j := i
		for j < size && !gone[j] {
			j++
		}
		out = append(out, unit{from: i, to: j})
		i = j
	}
	return out
}

// writeAt places data at off, growing dst and zero-filling any gap, which is
// what a real filesystem does for a write past the end of a file. This is
// distinct from a lost range: a gap is a hole the filesystem itself creates,
// never bytes the record says landed.
func writeAt(dst []byte, off int64, data []byte) []byte {
	end := off + int64(len(data))
	if int64(len(dst)) < end {
		grown := make([]byte, end)
		copy(grown, dst)
		dst = grown
	}
	copy(dst[off:end], data)
	return dst
}

// resize grows or shrinks dst to size, zero-filling any growth, which is
// what ftruncate does past the current end of a file.
func resize(dst []byte, size int64) []byte {
	if int64(len(dst)) >= size {
		return append([]byte(nil), dst[:size]...)
	}
	grown := make([]byte, size)
	copy(grown, dst)
	return grown
}
