package leak

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Counter is what fs.Fault, alloc.Fault and sql.Fault answer.
type Counter interface {
	Outstanding() int
	MaxOutstanding() int
}

// namer is what fs.Fault also answers: the names behind the count. It has
// no exported name, because no exported function of this package names it
// -- a caller reaches it only by giving Report a Counter that also happens
// to answer OpenPaths, never by naming the interface itself.
type namer interface {
	OpenPaths() []string
}

// Report answers the second question for every counter, and refuses to
// answer it for a counter whose count never rose. It returns one sentence
// per problem, and nothing when there is none.
//
// A counter that also has an OpenPaths() []string method gives the names
// behind the count: Outstanding gives the number, OpenPaths gives the
// names, and when the two disagree, Report states both rather than
// choosing one -- "still holds 2, and OpenPaths names 3: a, b, c".
//
// A nil counter gives its own sentence rather than a panic, so one bad
// argument in a longer list does not take the whole check down with it.
//
// The two checks below are independent, and both read the counter rather
// than each other, because MaxOutstanding()==0 and Outstanding()>0 can
// never both hold for a counter that keeps its own contract --
// MaxOutstanding is the high-water mark of Outstanding, so a real answer of
// zero for the first makes the second zero too. Nothing here assumes the
// contract holds: each sentence fires on its own reading of the counter,
// whatever the other reading says.
func Report(counters ...Counter) []string {
	var out []string
	for _, c := range counters {
		if c == nil {
			out = append(out, "leak: a nil counter was given, so this check proved nothing")
			continue
		}
		if c.MaxOutstanding() == 0 {
			out = append(out, fmt.Sprintf("%T never held anything, so its leak check proved nothing", c))
		}
		if n := c.Outstanding(); n > 0 {
			if nm, ok := c.(namer); ok {
				paths := append([]string(nil), nm.OpenPaths()...)
				sort.Strings(paths)
				if len(paths) == n {
					out = append(out, fmt.Sprintf("%T still holds %d: %s", c, n, strings.Join(paths, ", ")))
				} else {
					out = append(out, fmt.Sprintf("%T still holds %d, and OpenPaths names %d: %s", c, n, len(paths), strings.Join(paths, ", ")))
				}
			} else {
				out = append(out, fmt.Sprintf("%T still holds %d", c, n))
			}
		}
	}
	return out
}

// Check is Report on a testing.TB: one t.Errorf per sentence. Given no
// counter, it fails too, because a check that checked nothing reads exactly
// like a check that passed.
func Check(t testing.TB, counters ...Counter) {
	t.Helper()
	if len(counters) == 0 {
		t.Errorf("leak.Check: no counter given, so this check proved nothing")
		return
	}
	for _, s := range Report(counters...) {
		t.Errorf("%s", s)
	}
}
