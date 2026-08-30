package crash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// maxNameLen bounds a state's subtest name. Provisional; Task 15 measures it.
const maxNameLen = 80

// index builds the entry lookup every naming function needs.
func index(entries []entry) map[int]entry {
	byIndex := make(map[int]entry, len(entries))
	for _, e := range entries {
		byIndex[e.n] = e
	}
	return byIndex
}

// opNames covers every kind, not only the mutating ones. occurrences numbers
// an entry within its own (path, kind) sequence: a read and an open on the
// same path are different operations, and a kind missing from this map would
// fall back to the same empty string, silently merging their counts.
var opNames = map[kind]string{
	kRead: "read", kOpen: "open", kCreate: "create", kWrite: "write",
	kTruncate: "truncate", kRename: "rename", kRemove: "remove",
	kMkdir: "mkdir", kSync: "sync",
}

// occurrences numbers each entry within its own (path, kind) sequence, from 1.
// That is what makes a name survive an extra write somewhere else: "the second
// write to data.tmp" does not move when an unrelated file is touched first.
func occurrences(entries []entry) map[int]int {
	seen := map[string]int{}
	out := make(map[int]int, len(entries))
	for _, e := range entries {
		key := e.path + "\x00" + opNames[e.k]
		seen[key]++
		out[e.n] = seen[key]
	}
	return out
}

// unitName names one unit structurally: the file, the operation, and which
// occurrence of that operation on that file. A sector carries its index.
//
// The occurrence map is rebuilt here rather than cached, because a caller
// naming one unit in a failure message has no reason to hold it. stateName
// calls this once per lost unit, which makes one state's name cost
// O(units-in-state x entries-in-record x log(entries-in-record)) -- the sort
// below runs again on every call. That is worth knowing if a record ever
// grows into the thousands of entries with large lost sets, but it is not
// fixed here: see the task report.
func unitName(byIndex map[int]entry, u unit) string {
	e, ok := byIndex[u.entry]
	if !ok {
		return fmt.Sprintf("entry%d", u.entry)
	}

	var all []entry
	for _, v := range byIndex {
		all = append(all, v)
	}
	// Go randomises map iteration on purpose. Sorting by n, the entry's
	// original sequence position, is what makes the occurrence numbering
	// below the same on every call -- without it, "the second write" could
	// name a different write depending on which order range happened to
	// visit the map in.
	sort.Slice(all, func(i, j int) bool { return all[i].n < all[j].n })
	nth := occurrences(all)[u.entry]

	name := fmt.Sprintf("%s:%s%d", safe(e.path), opNames[e.k], nth)
	if !u.whole() {
		// The sector index within the write, not within the file, because the
		// write is what the name already identifies.
		name += fmt.Sprintf(".s%d", u.sect)
	}
	return name
}

// safe removes the characters that would change a subtest name's structure. A
// slash would create another level of nesting, which go test -run cannot be
// pointed at as one unit, so it is turned into a pipe instead.
//
// The whitespace pass mirrors testing.rewrite, in $GOROOT/src/testing/match.go:
// that function is what actually builds the subtest name -run matches
// against, and it replaces every rune in its own isSpace set -- not only the
// ASCII space -- with an underscore. Doing the ASCII case alone here would
// make a path holding a tab or a newline render one way in this package's
// own output and a different way in the name go test uses, and the two would
// stop matching, which is the one property this whole file exists to keep.
//
// This does not mirror testing.rewrite's other half, which escapes
// non-printable runes with strconv.QuoteRune. A file name a scenario chose
// is close to always printable, and reproducing that escaping byte for byte
// buys no case this package's callers are expected to hit.
func safe(p string) string {
	p = strings.ReplaceAll(p, "/", "|")

	var b strings.Builder
	b.Grow(len(p))
	for _, r := range p {
		if isTestSpace(r) {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isTestSpace is testing.isSpace, copied rather than approximated with
// unicode.IsSpace: Go's own comment on the original says it is deliberately
// NOT the same as the Unicode Z class, so deriving this from a general
// Unicode predicate would drift from what -run actually does.
func isTestSpace(r rune) bool {
	if r < 0x2000 {
		switch r {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680:
			return true
		}
	} else {
		if r <= 0x200a {
			return true
		}
		switch r {
		case 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
			return true
		}
	}
	return false
}

// stateName names the set of units a state lost.
//
// When the full list would be too long to read, the name becomes a count and
// a stable hash of the structural keys. A name a person cannot read is still
// a name go test -run can match, and the detail belongs in the failure
// message either way.
func stateName(byIndex map[int]entry, lost []unit) string {
	if len(lost) == 0 {
		return "lost=none"
	}

	parts := make([]string, 0, len(lost))
	for _, u := range lost {
		parts = append(parts, unitName(byIndex, u))
	}
	sort.Strings(parts)

	full := "lost=" + strings.Join(parts, "+")
	if len(full) <= maxNameLen {
		return full
	}

	// 16 hex characters is 64 bits of the digest. maxNameLen leaves plenty of
	// room for more (a count prefix is at most a handful of digits), and the
	// count alone cannot rescue a collision: two different lost sets of the
	// same size hash to the same "Nunits:" prefix regardless, so the hash is
	// the only thing standing between two different failures and one -run
	// pattern. 8 hex characters (32 bits) hits a 50% collision chance around
	// 2^16 distinct long names, which a large exhaustive sweep can reach; 64
	// bits pushes that bound out past 2^32 and is still a short name.
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("lost=%dunits:%s", len(lost), hex.EncodeToString(h[:])[:16])
}

// pointName names the crash point: the operation the process died after.
func pointName(byIndex map[int]entry, k int) string {
	return "after=" + unitName(byIndex, unit{entry: k})
}
