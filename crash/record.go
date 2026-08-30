package crash

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	faultfs "github.com/dd0wney/fault/fs"
)

// maxBytes bounds what one record holds. A record that quietly dropped its
// tail would build states no crash can produce, and the sweep would still
// pass, so passing the bound fails the test instead.
//
// The value is provisional. Task 15 replaces it with a number measured from
// the four reference stores.
const maxBytes = 64 << 20

type kind int

const (
	kRead kind = iota
	kOpen
	kCreate
	kWrite
	kTruncate
	kRename
	kRemove
	kMkdir
	kSync
)

// mutates reports whether an operation of this kind changes state, and so
// whether a crash point sits after it. A read and a plain open change nothing.
func (k kind) mutates() bool {
	switch k {
	case kCreate, kWrite, kTruncate, kRename, kRemove, kMkdir:
		return true
	}
	return false
}

// entry is one call the scenario made.
type entry struct {
	n     int // index in the full operation sequence, from 1
	k     kind
	path  string // slash-separated, relative to root
	to    string // rename target, slash-separated and relative
	off   int64  // the file offset at the time of the call
	size  int64  // truncate target
	data  []byte // only the bytes that actually landed
	dir   bool   // a sync on a handle opened on a directory
	needs []int  // indexes this entry depends on
}

// Recorder serves every call for real and records what changed. It is an
// fs.FS, so a scenario written against the fs adapter needs no change.
type Recorder struct {
	mu      sync.Mutex
	base    faultfs.FS
	root    string
	entries []entry
	bytes   int
	err     error // the first refusal; every later one is redundant

	// origin maps a live path to the index of the entry that made it exist.
	// A path absent from the map existed in the initial snapshot, so nothing
	// depends on its creation.
	origin map[string]int

	// snap is the tree under root at the moment Record was called. It is
	// written once, here, and never again, so no lock guards reading it
	// afterwards. The replay starts from this copy rather than from root
	// itself, so a change a later call makes to root cannot be mistaken for
	// what was already there.
	snap tree

	// live is the set of paths that exist right now, under r.mu. It starts
	// as snap's keys and is kept exactly in step with every create, mkdir,
	// rename and remove this run makes. exists answers from this, not from
	// snap: snap is fixed at Record time, so it never learns that a run
	// removed a path it held, or created a path it did not.
	live map[string]struct{}
}

// Record wraps base and records every change under root, so a crash state can
// be rebuilt.
//
// It copies the tree under root once, before anything else runs, and holds
// that copy as snap. This is what lets a test build its initial state before
// recording: the setup never enters the crash space, because the replay
// starts from the snapshot rather than from root, and a state a power cut
// could leave is only ever one Record made.
func Record(base faultfs.FS, root string) *Recorder {
	r := &Recorder{base: base, root: root, origin: map[string]int{}}
	snap, err := readTree(root)
	if err != nil {
		r.fail(fmt.Errorf("crash: cannot snapshot the record root %q: %w", root, err))
		snap = tree{}
	}
	r.snap = snap
	r.live = make(map[string]struct{}, len(snap))
	for p := range snap {
		r.live[p] = struct{}{}
	}
	return r
}

// failure reports the first refusal, or nil. Run turns it into t.Fatal. It is
// held rather than returned from each method because a scenario is written to
// handle filesystem errors, and a refusal is not one of those -- it is this
// package saying it cannot describe what happened.
func (r *Recorder) failure() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// rel converts an absolute or relative name into the slash-separated path the
// record uses. A name outside root is a refusal, because it cannot be rebuilt.
func (r *Recorder) rel(name string) (string, bool) {
	p, err := filepath.Rel(r.root, name)
	if err != nil || p == ".." || len(p) > 2 && p[:3] == ".."+string(filepath.Separator) {
		r.fail(fmt.Errorf("crash: %q is outside the recorded root %q, so no crash state can be rebuilt for it", name, r.root))
		return "", false
	}
	return filepath.ToSlash(p), true
}

func (r *Recorder) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

// add appends an entry and returns its index. The caller holds r.mu.
func (r *Recorder) add(e entry) int {
	e.n = len(r.entries) + 1
	r.bytes += len(e.data)
	if r.bytes > maxBytes {
		r.fail(fmt.Errorf("crash: the record passed its %d byte budget, so it cannot describe the run", maxBytes))
	}
	r.entries = append(r.entries, e)
	return e.n
}

func (r *Recorder) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, _ := r.rel(name)
	// exists must run before the base call below: O_CREATE can bring p into
	// existence, and asking after that call would see the effect of THIS
	// call rather than what came before it. The lock spans both, and the
	// base call itself, so two callers opening the same new path cannot both
	// see "does not exist" and both record a create. Read and Write already
	// hold their lock across the base call for the same reason; this is that
	// same shape.
	existed := r.exists(p)

	f, err := r.base.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}

	// A name outside root is held as a refusal and the call is still SERVED,
	// which is what the other five methods do.
	//
	// Returning an invented *os.PathError here would be worse than the problem
	// it reports. The scenario would take an error branch it never takes in
	// production, so the record would describe a different program. The held
	// refusal already guarantees that Run fails before any state is built, so
	// nothing recorded under the empty path is ever used.
	//
	// This package records. It does not inject.

	// Whether the handle is a directory decides which pending set a Sync on it
	// clears. The recorder asks the base directly rather than through itself,
	// because its own bookkeeping is not an operation the scenario performed
	// and must not take an index.
	isDir := false
	if st, statErr := r.base.Stat(name); statErr == nil {
		isDir = st.IsDir()
	}

	switch {
	case flag&os.O_CREATE != 0 && !existed:
		n := r.add(entry{k: kCreate, path: p})
		r.origin[p] = n
		r.live[p] = struct{}{}
	default:
		r.add(entry{k: kOpen, path: p})
	}

	return &file{r: r, base: f, path: p, dir: isDir}, nil
}

// exists reports whether p is present right now, so an O_CREATE open records
// a create only when it actually creates something. It answers from live,
// not from snap: snap is fixed at Record time, and this run can both create
// a path snap never held and remove a path snap did hold, so snap alone
// answers for the wrong instant once the run has changed the namespace.
func (r *Recorder) exists(p string) bool {
	_, ok := r.live[p]
	return ok
}

func (r *Recorder) Remove(name string) error {
	r.mu.Lock()
	p, ok := r.rel(name)
	if ok {
		r.add(entry{k: kRemove, path: p, needs: r.dependsOn(p)})
		delete(r.origin, p)
		delete(r.live, p)
	}
	r.mu.Unlock()
	return r.base.Remove(name)
}

func (r *Recorder) Rename(oldname, newname string) error {
	r.mu.Lock()
	from, okFrom := r.rel(oldname)
	to, okTo := r.rel(newname)
	if okFrom && okTo {
		n := r.add(entry{k: kRename, path: from, to: to, needs: r.dependsOn(from)})
		delete(r.origin, from)
		r.origin[to] = n
		delete(r.live, from)
		r.live[to] = struct{}{}
	}
	r.mu.Unlock()
	return r.base.Rename(oldname, newname)
}

func (r *Recorder) Stat(name string) (os.FileInfo, error) {
	r.mu.Lock()
	if p, ok := r.rel(name); ok {
		r.add(entry{k: kRead, path: p})
	}
	r.mu.Unlock()
	return r.base.Stat(name)
}

func (r *Recorder) MkdirAll(name string, perm os.FileMode) error {
	r.mu.Lock()
	if p, ok := r.rel(name); ok {
		n := r.add(entry{k: kMkdir, path: p})
		r.origin[p] = n
		r.live[p] = struct{}{}
	}
	r.mu.Unlock()
	return r.base.MkdirAll(name, perm)
}

func (r *Recorder) ReadDir(name string) ([]os.DirEntry, error) {
	r.mu.Lock()
	if p, ok := r.rel(name); ok {
		r.add(entry{k: kRead, path: p})
	}
	r.mu.Unlock()
	return r.base.ReadDir(name)
}

// dependsOn returns the indexes an operation on p needs. The caller holds
// r.mu. A path that existed in the initial snapshot has no origin entry, so
// nothing depends on its creation: it arrives durable and cannot be lost.
func (r *Recorder) dependsOn(p string) []int {
	if n, ok := r.origin[p]; ok {
		return []int{n}
	}
	return nil
}

// file is an open handle whose operations continue the same sequence the
// filesystem started.
type file struct {
	r    *Recorder
	base faultfs.File
	path string
	dir  bool
	off  int64 // tracked here because fs.File has no Seek
}

// The lock spans the base call as well as the record.
//
// fs.FS requires an implementation to be safe for concurrent use, and an
// unlocked `f.off += n` is a data race that the race detector finds in
// seconds once two goroutines share one handle. Serialising also buys the
// property that matters more: the offset this handle reports and the
// position the base actually wrote at cannot disagree, because nothing can
// interleave between them. A test tool may pay a mutex for that.
func (f *file) Read(b []byte) (int, error) {
	f.r.mu.Lock()
	defer f.r.mu.Unlock()

	n, err := f.base.Read(b)
	f.r.add(entry{k: kRead, path: f.path})
	// A read advances the offset, so a later write lands after it. Missing
	// this puts every write in the record at the wrong place.
	f.off += int64(n)
	return n, err
}

func (f *file) Write(b []byte) (int, error) {
	f.r.mu.Lock()
	defer f.r.mu.Unlock()

	n, err := f.base.Write(b)
	if n > 0 {
		data := make([]byte, n)
		copy(data, b[:n])
		f.r.add(entry{
			k:     kWrite,
			path:  f.path,
			off:   f.off,
			data:  data, // only what landed, never what was offered
			needs: f.r.dependsOn(f.path),
		})
		f.off += int64(n)
	}
	return n, err
}

func (f *file) Sync() error {
	f.r.mu.Lock()
	f.r.add(entry{k: kSync, path: f.path, dir: f.dir})
	f.r.mu.Unlock()
	return f.base.Sync()
}

func (f *file) Truncate(size int64) error {
	f.r.mu.Lock()
	f.r.add(entry{k: kTruncate, path: f.path, size: size, needs: f.r.dependsOn(f.path)})
	f.r.mu.Unlock()
	return f.base.Truncate(size)
}

// Close records an index and nothing else. POSIX does not make close(2) flush,
// so a close clears no pending set. A store that treats it as a flush holds
// the exact defect this package exists to find.
func (f *file) Close() error {
	f.r.mu.Lock()
	f.r.add(entry{k: kOpen, path: f.path})
	f.r.mu.Unlock()
	return f.base.Close()
}
