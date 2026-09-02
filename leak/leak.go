package leak

import "testing"

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
func Report(counters ...Counter) []string {
	// TODO(leak): not yet implemented. A placeholder sentence, wrong for
	// every fixture, so a test that expects a real reading fails instead of
	// passing on an accidental nil.
	return []string{"leak.Report: not yet implemented"}
}

// Check is Report on a testing.TB: one t.Errorf per sentence. Given no
// counter, it fails too, because a check that checked nothing reads exactly
// like a check that passed.
func Check(t testing.TB, counters ...Counter) {
	t.Helper()
	// TODO(leak): not yet implemented.
}
