package crash

// Entry is the test-visible shape of a record entry. The internal type stays
// unexported, because an adapter never reads one and a caller never builds one.
type Entry struct {
	N    int
	Path string
}

// Entries exposes the record to tests in package crash_test.
func Entries(r *Recorder) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, len(r.entries))
	for i, e := range r.entries {
		out[i] = Entry{N: e.n, Path: e.path}
	}
	return out
}

// Failure exposes the held refusal to tests.
func Failure(r *Recorder) error { return r.failure() }
