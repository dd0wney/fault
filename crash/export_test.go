package crash

// Entry is the test-visible shape of a record entry. The internal type stays
// unexported, because an adapter never reads one and a caller never builds one.
//
// Kind, Off and Data are exposed because they carry the two properties the
// package depends on most: that a write records only the bytes that landed,
// and that a read advances the offset a later write is recorded at. A field
// no test can see is a field no test can guard.
type Entry struct {
	N    int
	Kind string
	Path string
	Off  int64
	Data []byte
}

// kindNames gives each kind a stable, test-visible name. It lives here and
// not on kind itself, because production code never needs to print a kind.
var kindNames = map[kind]string{
	kRead:     "read",
	kOpen:     "open",
	kCreate:   "create",
	kWrite:    "write",
	kTruncate: "truncate",
	kRename:   "rename",
	kRemove:   "remove",
	kMkdir:    "mkdir",
	kSync:     "sync",
}

// Entries exposes the record to tests in package crash_test.
func Entries(r *Recorder) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.entries))
	for i, e := range r.entries {
		out[i] = Entry{N: e.n, Kind: kindNames[e.k], Path: e.path, Off: e.off, Data: e.data}
	}
	return out
}

// Failure exposes the held refusal to tests.
func Failure(r *Recorder) error { return r.failure() }

// Needs exposes each entry's dependency list, in entry order.
func Needs(r *Recorder) [][]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]int, len(r.entries))
	for i, e := range r.entries {
		out[i] = append([]int(nil), e.needs...)
	}
	return out
}
