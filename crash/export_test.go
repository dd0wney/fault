package crash

import (
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

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

// ErrNoMutations and ErrTooManyStates expose two of the four refusals to tests
// in package crash_test. A test that asked only for a non-nil error could not
// tell one refusal from another, and every one of them fails the same way.
var (
	ErrNoMutations   = errNoMutations
	ErrTooManyStates = errTooManyStates
)

// Plan exposes the state names to tests, without a *testing.T. The names are
// what go test -run matches, so a test that reads them reads exactly what a
// developer would have to type to re-run one state.
func Plan(r *Recorder, m Model) ([]string, error) {
	states, err := plan(r, m)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	byIndex := index(r.entries)
	r.mu.Unlock()

	out := make([]string, len(states))
	for i, s := range states {
		out[i] = pointName(byIndex, s.point) + "/" + s.name
	}
	return out, nil
}

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

// State is a test-visible candidate state.
//
// Name is the address go test -run matches, so a test that prints it prints
// exactly what a developer has to type to re-run the one state.
//
// Point is the entry index the crash sits after. A check whose legal answer
// depends on how far the scenario had already got cannot be written without
// it, and the four reference stores are that kind of check: a state from a
// crash inside the first save may hold the old value, and a state from a crash
// inside the second save may not.
type State struct {
	Name  string
	Point int

	tree tree
	root string
}

// FS materialises the state and returns an FS that serves it under the names
// the scenario used.
func (s State) FS(t *testing.T) faultfs.FS {
	t.Helper()
	dir := t.TempDir()
	if err := s.tree.writeTo(dir); err != nil {
		t.Fatalf("cannot materialise state %s: %v", s.Name, err)
	}
	return &remapFS{base: faultfs.OS(), from: s.root, to: dir}
}

// States exposes plan's output to a test that must assert a store FAILS, which
// cannot be done through Run without failing the enclosing test.
func States(r *Recorder, m Model) ([]State, error) {
	got, err := plan(r, m)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	byIndex := index(r.entries)
	root := r.root
	r.mu.Unlock()

	out := make([]State, len(got))
	for i, s := range got {
		out[i] = State{
			Name:  pointName(byIndex, s.point) + "/" + s.name,
			Point: s.point,
			tree:  s.result,
			root:  root,
		}
	}
	return out, nil
}
