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

// Namer is what fs.Fault also answers: the names behind the count.
type Namer interface {
	OpenPaths() []string
}

// Report answers the second question for every counter, and refuses to
// answer it for a counter whose count never rose. It returns one sentence
// per problem, and nothing when there is none.
//
// The two checks are independent, and both read the counter rather than
// each other, because MaxOutstanding()==0 and Outstanding()>0 can never
// both hold for a counter that keeps its own contract -- MaxOutstanding is
// the high-water mark of Outstanding, so a real answer of zero for the
// first makes the second zero too. Nothing here assumes the contract holds:
// each sentence fires on its own reading of the counter, whatever the
// other reading says.
func Report(counters ...Counter) []string {
	var out []string
	for _, c := range counters {
		if c.MaxOutstanding() == 0 {
			out = append(out, fmt.Sprintf("%T never held anything, so its leak check proved nothing", c))
		}
		if n := c.Outstanding(); n > 0 {
			if namer, ok := c.(Namer); ok {
				paths := append([]string(nil), namer.OpenPaths()...)
				sort.Strings(paths)
				out = append(out, fmt.Sprintf("%T still holds %d: %s", c, n, strings.Join(paths, ", ")))
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
