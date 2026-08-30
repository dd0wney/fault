package crash

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	faultfs "github.com/dd0wney/fault/fs"
)

// maxBytes bounds what one record holds. A record that quietly dropped its
// tail would build states no crash can produce, and the sweep would still
// pass, so passing the bound fails the test instead.
//
// MEASURED 2026-08-30: each of the four reference stores records 8 bytes for
// the two-save scenario, under Model{Sector: 4096}. That number says the bound
// is nowhere near the traffic of the scenarios this package is written for, so
// it is set for the record that runs away rather than for the ones measured.
// 64 MiB is where a record stops being a test fixture: the recorder holds every
// byte a scenario wrote, in memory, for the whole walk.
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
	p, ok := under(r.root, name)
	if !ok {
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

// THE ORDER RULE, obeyed by every method below.
//
// An entry is recorded only after the base call has returned nil. A call the
// filesystem refused changed nothing, so a record that holds it describes a
// run that did not happen, and every state built from it is a fiction that
// reads exactly like a finding.
//
// The measured case is the ordinary idiom `defer fsys.Remove(tmp)` after a
// successful rename. The remove fails with ENOENT, because the rename already
// moved the name. Recording it anyway put a phantom remove in the log, and the
// walk built a state in which NEITHER name held the value. No power cut can
// produce that state. A crash simulator that flags correct code is worse than
// none, because every later finding then needs a human to re-derive whether it
// is real.
//
// The lock still spans the base call. A check-then-act race was fixed once
// already -- two callers opening the same new path could both see "does not
// exist" and both record a create -- and TestConcurrentOpensOnOneNewPathRecord-
// ExactlyOneCreate is its regression guard. Only the decision moved.

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
	// clears, and how many bytes the file already holds decides where an
	// O_APPEND handle starts. The recorder asks the base directly rather than
	// through itself, because its own bookkeeping is not an operation the
	// scenario performed and must not take an index.
	//
	// The Stat runs AFTER the open on purpose: O_TRUNC empties the file as part
	// of the open, so the size before it is not the size the first write lands
	// at.
	isDir, size := false, int64(0)
	st, statErr := r.base.Stat(name)
	if statErr == nil {
		isDir, size = st.IsDir(), st.Size()
	}

	// O_APPEND puts every write at the current end of the file, so a handle
	// opened that way starts at the size the file already holds and not at
	// zero. Section 5.2 argues that tracking the offset by addition is sound
	// because fs.File has no Seek; O_APPEND reaches the same hazard through a
	// flag that ships today, and starting at the size is what closes it.
	//
	// A size the base will not give is a refusal, not a guess. Every offset
	// recorded through the handle would be wrong, and a record that is wrong in
	// silence is the failure this package exists to prevent.
	var off int64
	if flag&os.O_APPEND != 0 {
		if statErr != nil {
			r.fail(fmt.Errorf("crash: cannot size %q for an O_APPEND handle, so every offset recorded through it would be wrong: %w", name, statErr))
		}
		off = size
	}

	switch {
	case flag&os.O_CREATE != 0 && !existed:
		// The create depends on the mkdir of the directory that holds the name,
		// where this run made that directory. Without it the walk builds a file
		// inside a directory whose creation was lost, which section 6.1 calls
		// illegal -- and writeTo re-creates the parent, so no check can tell
		// that state from one that lost nothing while its name says otherwise.
		n := r.add(entry{k: kCreate, path: p, needs: r.dependsOn(path.Dir(p))})
		r.origin[p] = n
		r.live[p] = struct{}{}
	default:
		r.add(entry{k: kOpen, path: p})
	}

	// O_TRUNC resets the size of a file the open did not create, and it does so
	// before the caller writes a byte. Recording it as a truncate to 0 is what
	// lets the replay model a publish that overwrites a long value with a short
	// one. Without the entry the replay keeps the tail of the older value, the
	// replay control refuses the whole record, and the sweep cannot describe
	// the run at all.
	//
	// A path this open created is already empty, so no entry describes it, and
	// a second entry there would give the file two origins.
	//
	// It carries a dependency for the same reason every other truncate does: a
	// crash that loses the create of the file loses this operation on it too.
	if flag&os.O_TRUNC != 0 && existed {
		r.add(entry{k: kTruncate, path: p, size: 0, needs: r.dependsOn(p)})
	}

	return &file{r: r, base: f, path: p, dir: isDir, off: off}, nil
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
	defer r.mu.Unlock()

	// rel runs before the base call, so a name outside root is refused whether
	// or not the removal succeeds. The refusal only ever makes Run fail loudly.
	p, ok := r.rel(name)

	if err := r.base.Remove(name); err != nil {
		return err
	}
	if !ok {
		return nil
	}
	r.add(entry{k: kRemove, path: p, needs: r.dependsOn(p)})
	delete(r.origin, p)
	delete(r.live, p)
	return nil
}

func (r *Recorder) Rename(oldname, newname string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	from, okFrom := r.rel(oldname)
	to, okTo := r.rel(newname)

	if err := r.base.Rename(oldname, newname); err != nil {
		return err
	}
	if !okFrom || !okTo {
		return nil
	}
	// A rename needs the origin of the name it moves, and the mkdir of the
	// directory it moves INTO where this run made that directory. It places a
	// name in a directory exactly as a create does, so it carries the same
	// dependency for the same reason. split already treats both directories
	// alike when it decides durability.
	needs := r.dependsOn(from)
	needs = append(needs, r.dependsOn(path.Dir(to))...)

	n := r.add(entry{k: kRename, path: from, to: to, needs: needs})
	delete(r.origin, from)
	r.origin[to] = n
	delete(r.live, from)
	r.live[to] = struct{}{}
	return nil
}

func (r *Recorder) Stat(name string) (os.FileInfo, error) {
	r.mu.Lock()
	if p, ok := r.rel(name); ok {
		r.add(entry{k: kRead, path: p})
	}
	r.mu.Unlock()
	return r.base.Stat(name)
}

// MkdirAll records one entry for each level it actually created, outermost
// first, and each level depends on its parent where this run made the parent.
//
// One entry for the deepest path alone described a directory that appears with
// no directory above it. The positive control refused every such record with
// "missing: a", so the package could record no scenario that builds a directory
// tree at all.
//
// A level already present is not created, so it takes no entry: two entries for
// one name would give it two origins, and a crash matches neither.
func (r *Recorder) MkdirAll(name string, perm os.FileMode) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	p, ok := r.rel(name)

	if err := r.base.MkdirAll(name, perm); err != nil {
		return err
	}
	if !ok {
		return nil
	}
	for _, lvl := range levels(p) {
		if r.exists(lvl) {
			continue
		}
		n := r.add(entry{k: kMkdir, path: lvl, needs: r.dependsOn(path.Dir(lvl))})
		r.origin[lvl] = n
		r.live[lvl] = struct{}{}
	}
	return nil
}

// levels lists p and every directory above it, outermost first, which is the
// order MkdirAll builds them in.
//
// The record root itself is not a level. It is not a key in the tree, nothing
// this package records can create it, and an entry for it would put a "." into
// every rebuilt state.
func levels(p string) []string {
	if p == "" || p == "." {
		return nil
	}
	parts := strings.Split(p, "/")
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
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
	// off is tracked here because fs.File has no Seek. It starts at zero, or
	// at the file's size when the open carried O_APPEND, and moves only by the
	// bytes this handle's own reads and writes carry.
	off int64
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

// Sync records nothing when the base call fails. A sync that failed made
// nothing durable, so a record that holds it lets the model clear a pending set
// the disk still holds, and every state that a crash there could really leave
// stops being generated. That is an under-report, and a missed defect is
// invisible.
// Seek and WriteAt are the OPTIONAL capabilities of spec §12. The recorder
// offers both when its base does, and refuses otherwise, wrapping
// errors.ErrUnsupported so the standard idiom detects it.
//
// Together they close the hole spec §5.2 named: the offset was tracked by
// addition, which was sound only while nothing could move it except reads and
// writes. The rule is now "the last seek result, plus the bytes moved since",
// and the whole-record control of §9.2 is what catches a mistake in it.

func (f *file) unsupported(op string) error {
	return &os.PathError{
		Op:   op,
		Path: f.path,
		Err:  fmt.Errorf("the recorded filesystem cannot %s: %w", op, errors.ErrUnsupported),
	}
}

// Seek takes an index and records no state change, because it changes none.
//
// The offset comes from the BASE's answer, not from arithmetic here. io.Seeker
// returns the new offset relative to the start of the file, so there is nothing
// to model: no whence values, and no file size for SEEK_END.
func (f *file) Seek(offset int64, whence int) (int64, error) {
	s, ok := f.base.(interface {
		Seek(offset int64, whence int) (int64, error)
	})
	if !ok {
		return 0, f.unsupported("seek")
	}

	f.r.mu.Lock()
	defer f.r.mu.Unlock()

	n, err := s.Seek(offset, whence)
	f.r.add(entry{k: kRead, path: f.path})
	if err == nil {
		f.off = n
	}
	return n, err
}

// WriteAt records the offset the CALLER gave and never touches the handle
// position, which is what a positional write means.
//
// That is also why it costs the model nothing: the durability split, the loss
// units, the closure and the replay all key on entry.off and do not care where
// the offset came from.
func (f *file) WriteAt(b []byte, off int64) (int, error) {
	w, ok := f.base.(interface {
		WriteAt(p []byte, off int64) (int, error)
	})
	if !ok {
		return 0, f.unsupported("writeat")
	}

	f.r.mu.Lock()
	defer f.r.mu.Unlock()

	n, err := w.WriteAt(b, off)
	if n > 0 {
		data := make([]byte, n)
		copy(data, b[:n])
		f.r.add(entry{
			k:     kWrite,
			path:  f.path,
			off:   off,
			data:  data, // only what landed, never what was offered
			needs: f.r.dependsOn(f.path),
		})
	}
	return n, err
}

func (f *file) Sync() error {
	f.r.mu.Lock()
	defer f.r.mu.Unlock()

	if err := f.base.Sync(); err != nil {
		return err
	}
	f.r.add(entry{k: kSync, path: f.path, dir: f.dir})
	return nil
}

func (f *file) Truncate(size int64) error {
	f.r.mu.Lock()
	defer f.r.mu.Unlock()

	if err := f.base.Truncate(size); err != nil {
		return err
	}
	f.r.add(entry{k: kTruncate, path: f.path, size: size, needs: f.r.dependsOn(f.path)})
	return nil
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
