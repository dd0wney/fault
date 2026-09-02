# fault/crash Implementation Plan

> **Status, recorded 2026-09-02:** every task below shipped, the last of them
> in `e2c36d0`, and the package has run in CI since 2026-08-30. The checkboxes
> were never marked, so a reader who trusts only them would call this plan
> open. It is not. The record of what the package still does not do is in
> `crash/doc.go` under "Limitations", not here.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `github.com/dd0wney/fault/crash`, which reopens a store's on-disk state as though the process had died at each point, so a test can find durability defects that a returned-error sweep cannot reach.

**Architecture:** A recorder wraps a real filesystem, serves every call for real, and appends one entry per call. The scenario runs **once**. A model then turns that record plus a crash point into a set of legal on-disk states, and each state is rebuilt into a fresh directory and handed to a check callback in its own `t.Run` subtest. Replaying the whole record must reproduce the directory the scenario actually wrote, and that identity is asserted before any state is built.

**Tech Stack:** Go 1.23, standard library only. `github.com/dd0wney/fault/fs` for the `FS` and `File` interfaces. No third-party packages, ever.

**Spec:** `docs/design/2026-08-30-crash-simulation-design.md`

## Global Constraints

Every task's requirements implicitly include this section.

- **Module floor is `go 1.23.0`.** Do not use a standard library symbol added after 1.23. `os.Root` arrived in 1.24 and is therefore forbidden.
- **Zero external dependencies.** `scripts/no-external-deps.sh` is a CI gate. `crash` may import the standard library and `github.com/dd0wney/fault/fs`, and nothing else.
- **`crash` does not import the core `fault` package.** Record-once uses no `Points` and no `Trip`. If an import of the core appears, stop and raise it — it means the design drifted.
- **Exported surface is exactly seven names:** `Record`, `Recorder`, `Run`, `Model`, `Cover`, `Exhaustive`, `Prefixes`. Adding an eighth needs a decision recorded in the spec, not a commit.
- **`gofmt` clean.** CI checks formatting. Run `gofmt -l .` before every commit and expect no output.
- **`go vet ./...` must pass before committing.** NOT `golangci-lint`: see the
  note below.
- **`golangci-lint` cannot run in this environment and is not a gate here.**
  The installed binary is 2.12.2, built with go1.26.3, and it fails to typecheck
  the Go 1.27.0 standard library (`internal/poll/splice_linux.go:237: unknown
  field rfd`) before it reaches any repository code. CI does not run it either —
  the four jobs are `test`, `checks`, `tools` and `mutation`, and none invokes
  it. Do not report a `golangci-lint` pass you did not get, and do not skip
  `go vet`, which does work and which CI does run.
- **CI is 3 operating systems × 2 Go versions.** Local gates say nothing about Windows. Never report CI from a local run — read `gh run view --json conclusion`.
- **Every gate gets a selftest that runs first.** A gate nobody has watched fail is not a gate.
- **No number in a document that was not produced by running the command that yields it.** This includes test counts, state counts and percentages.
- **Comments explain why, not what.** Match the density and voice of `fs/fault.go`, which is the house style for this module.
- **Paths inside the record are slash-separated and relative to `root`.** Convert with `filepath.ToSlash`. A Windows backslash must never reach a state name, or the same test gets two names on two platforms.

---

## File Structure

| File | Responsibility |
|---|---|
| `crash/model.go` | `Model`, `Cover`, `Exhaustive`, `Prefixes`. No behaviour. |
| `crash/record.go` | `Record`, `Recorder`, the `entry` log, the `fs.FS` and `fs.File` wrappers, offset tracking, `needs`. |
| `crash/snapshot.go` | `tree`, reading a directory into one, writing one out, comparing two. |
| `crash/rebuild.go` | Replaying a chosen entry set onto a snapshot to produce a `tree`. |
| `crash/state.go` | Durable/pending split, loss units, the `needs` closure, cover enumeration, deduplication. |
| `crash/name.go` | Structural names for a unit, a crash point and a state. |
| `crash/run.go` | `Run`, the internal `plan` that returns diagnostics, the remapping `fs.FS`. |
| `crash/doc.go` | Package documentation: the contract, the limits, the third loop. |
| `crash/export_test.go` | Internal access for tests, in the manner of the module's existing `export_test.go`. |

Test files sit beside the file they exercise. `crash/stores_test.go` holds the four reference stores, because they are fixtures shared by several test files.

---

### Task 1: Model and Cover

**Files:**
- Create: `crash/model.go`
- Test: `crash/model_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `type Model struct { Sector int; SplitAtCrashOnly bool; MetadataDurable bool; Cover Cover }`, `type Cover int`, `const Exhaustive Cover = 0`, `const Prefixes Cover = 1`.

- [ ] **Step 1: Write the failing test**

```go
package crash

import "testing"

// The zero Model must be the STRICT one, not the empty one. A caller who
// writes crash.Model{} gets whole-call loss, the POSIX metadata rule, and an
// exhaustive walk. This mirrors the core's zero Points, which arms nothing
// because counting happens before comparing.
func TestTheZeroModelIsTheStrictOne(t *testing.T) {
	var m Model

	if m.Sector != 0 {
		t.Errorf("Sector = %d, want 0 so a whole Write call is the unit", m.Sector)
	}
	if m.SplitAtCrashOnly {
		t.Error("SplitAtCrashOnly = true, want false so every pending write may split")
	}
	if m.MetadataDurable {
		t.Error("MetadataDurable = true, want false so the POSIX rule is the default")
	}
	if m.Cover != Exhaustive {
		t.Errorf("Cover = %v, want Exhaustive as the zero value", m.Cover)
	}
}

func TestCoverValuesAreDistinct(t *testing.T) {
	if Exhaustive == Prefixes {
		t.Fatal("Exhaustive and Prefixes are the same value")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestTheZeroModel|TestCoverValues' -v`
Expected: FAIL to build with `undefined: Model`, `undefined: Exhaustive`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package crash rebuilds a store's on-disk state as a crash would leave it.
package crash

// Cover chooses which candidate states a sweep visits.
type Cover int

const (
	// Exhaustive visits every legal subset of the pending units. It carries a
	// cap, and passing the cap fails the test rather than visiting a subset,
	// because a partial walk reports a pass the same way a complete one does.
	//
	// It is first in the block on purpose, so it is the zero value.
	Exhaustive Cover = iota

	// Prefixes visits the states in which the pending units persist in log
	// order, so a state is a prefix of the pending sequence. It costs u+1
	// states rather than 2^u, and it is the deterministic escape from the cap.
	Prefixes
)

// Model says what a crash can lose.
//
// The zero Model is the strict one: a whole Write call is the unit, metadata
// obeys the POSIX rule, and the walk is exhaustive. A caller who has not
// thought about it gets the most demanding model rather than the emptiest one.
type Model struct {
	// Sector is the unit a pending write can lose, in bytes. Zero loses a
	// whole Write call.
	Sector int

	// SplitAtCrashOnly limits sector splitting to the write at the crash
	// point. Every earlier pending write is then lost whole.
	SplitAtCrashOnly bool

	// MetadataDurable makes a create, rename, remove or mkdir durable as soon
	// as it returns, instead of pending until the parent directory is synced.
	// Set it for a store that must also run on Windows, where a directory
	// handle cannot be synced.
	MetadataDurable bool

	Cover Cover
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -run 'TestTheZeroModel|TestCoverValues' -v`
Expected: PASS, 2 tests.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/model.go crash/model_test.go
git commit -m "feat(crash): the model, whose zero value is the strict one"
```

---

### Task 2: The entry log, and a Recorder that passes everything through

**Files:**
- Create: `crash/record.go`, `crash/export_test.go`
- Test: `crash/record_test.go`

**Interfaces:**
- Consumes: `fs.FS`, `fs.File` from `github.com/dd0wney/fault/fs`.
- Produces:
  - `func Record(base fs.FS, root string) *Recorder`
  - `type Recorder struct{ ... }`, implementing `fs.FS`
  - `type kind int` with `kRead, kOpen, kCreate, kWrite, kTruncate, kRename, kRemove, kMkdir, kSync`
  - `func (k kind) mutates() bool`
  - `type entry struct { n int; k kind; path, to string; off, size int64; data []byte; dir bool; needs []int }`
  - `func (r *Recorder) failure() error` — the first refusal, or nil
  - test-only: `func Entries(r *Recorder) []Entry` in `export_test.go`

- [ ] **Step 1: Write the failing test**

```go
package crash_test

import (
	"os"
	"path/filepath"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
	"github.com/dd0wney/fault/crash"
)

// Every call gets an index, including one that changes nothing. The index is
// what keeps a crash point's number equal to the number fault.Sweep would give
// the same operation, which is the whole of what this package owes a later
// version that walks crash points through an injected error run.
func TestEveryCallGetsAnIndexIncludingAReadOne(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	name := filepath.Join(dir, "a")
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := f.Read(make([]byte, 1)); err != nil && err.Error() != "EOF" {
		// a read past the end is fine; the point is that it is counted
		_ = err
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := crash.Entries(rec)
	if len(got) < 4 {
		t.Fatalf("got %d entries, want at least 4 (open, write, read, close)", len(got))
	}
	for i, e := range got {
		if e.N != i+1 {
			t.Errorf("entry %d has N = %d, want %d — indexes must be 1-based and gapless", i, e.N, i+1)
		}
	}
}

// Wrap, do not replace. Every call the recorder does not need to change is
// served for real, so the scenario writes real bytes to a real disk.
func TestTheRecorderServesEveryCallForReal(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	name := filepath.Join(dir, "a")
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Read it with the os package, bypassing the recorder entirely.
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("the recorder did not write real bytes: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("on disk = %q, want %q", got, "hello")
	}
}

// A name outside root cannot be rebuilt. Leaving it out of the record in
// silence would build states that no crash can produce, and the sweep would
// still pass. The refusal is held and reported, not panicked.
func TestANameOutsideRootIsRefused(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere")
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(outside, os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		_ = f.Close()
	}

	if crash.Failure(rec) == nil {
		t.Fatal("a write outside root left no refusal, so Run would report a pass")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestEveryCall|TestTheRecorderServes|TestANameOutside' -v`
Expected: FAIL to build with `undefined: crash.Record`.

- [ ] **Step 3: Write minimal implementation**

`crash/record.go`:

```go
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
	n     int    // index in the full operation sequence, from 1
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
}

// Record wraps base and records every change under root, so a crash state can
// be rebuilt.
//
// Task 4 adds the initial snapshot and extends this comment to say why it is
// taken. Until then this describes only what it does, because a comment that
// promises absent behaviour is a defect.
func Record(base faultfs.FS, root string) *Recorder {
	return &Recorder{base: base, root: root, origin: map[string]int{}}
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
	p, _ := r.rel(name)
	r.mu.Unlock()

	f, err := r.base.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}

	// A name outside root is held as a refusal and the call is still SERVED,
	// which is what the other five methods do.
	//
	// Returning an invented *os.PathError here would be worse than the problem
	// it reports. The scenario would take an error branch it never takes in
	// production, so the record -- the thing this package exists to capture --
	// would describe a different program. The held refusal already guarantees
	// that Run fails before any state is built, so nothing recorded under the
	// empty path is ever used.
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

	r.mu.Lock()
	defer r.mu.Unlock()
	existed := true
	if _, seen := r.origin[p]; !seen {
		// It may still have existed in the snapshot. Task 4 refines this.
		existed = flag&os.O_CREATE == 0
	}
	switch {
	case !existed && flag&os.O_CREATE != 0:
		n := r.add(entry{k: kCreate, path: p})
		r.origin[p] = n
	default:
		r.add(entry{k: kOpen, path: p})
	}

	return &file{r: r, base: f, path: p, dir: isDir}, nil
}

func (r *Recorder) Remove(name string) error {
	r.mu.Lock()
	p, ok := r.rel(name)
	if ok {
		r.add(entry{k: kRemove, path: p, needs: r.dependsOn(p)})
		delete(r.origin, p)
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
// unlocked `f.off += n` is a data race that the race detector finds in seconds
// once two goroutines share one handle. Serialising also buys the property that
// matters more: the offset this handle reports and the position the base
// actually wrote at cannot disagree, because nothing can interleave between
// them. A test tool may pay a mutex for that.
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
```

`crash/export_test.go`:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -run 'TestEveryCall|TestTheRecorderServes|TestANameOutside' -v`
Expected: PASS, 3 tests.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/record.go crash/record_test.go crash/export_test.go
git commit -m "feat(crash): the record, which counts every call and keeps only what landed"
```

---

### Task 3: `needs`, and the dependency a state must respect

**Files:**
- Modify: `crash/record.go` (extend `export_test.go` with `Needs`)
- Test: `crash/needs_test.go`

**Interfaces:**
- Consumes: Task 2's `entry`, `Recorder.origin`, `Recorder.dependsOn`.
- Produces: test-only `func Needs(r *Recorder) [][]int`, returning each entry's `needs` in order.

- [ ] **Step 1: Write the failing test**

```go
package crash_test

import (
	"os"
	"path/filepath"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
	"github.com/dd0wney/fault/crash"
)

// A write needs the create of the file it writes into. Without this, a state
// could hold data for a file that has no directory entry, which no power cut
// can produce. Findings against such a state are noise, and noise trains a
// reader to ignore the tool.
func TestAWriteNeedsTheCreateOfItsFile(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}

	needs := crash.Needs(rec)
	// entry 1 is the create, entry 2 is the write
	if len(needs) < 2 {
		t.Fatalf("got %d entries, want at least 2", len(needs))
	}
	if len(needs[1]) != 1 || needs[1][0] != 1 {
		t.Errorf("the write needs %v, want [1] — the index of the create", needs[1])
	}
}

// A rename needs the create of the name it moves, and it becomes the origin of
// the new name. A write after the rename therefore needs the rename.
func TestARenameCarriesTheOriginToTheNewName(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "tmp"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = f.Close()
	if err := rec.Rename(filepath.Join(dir, "tmp"), filepath.Join(dir, "data")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	needs := crash.Needs(rec)
	// 1 create tmp, 2 close, 3 rename
	if len(needs) < 3 {
		t.Fatalf("got %d entries, want at least 3", len(needs))
	}
	if len(needs[2]) != 1 || needs[2][0] != 1 {
		t.Errorf("the rename needs %v, want [1] — the create of tmp", needs[2])
	}
}

// A file that existed before Record has no create entry, so nothing depends on
// one. It arrives from the snapshot and is durable by construction. This is
// what lets a test build its initial state before recording.
func TestAPreExistingFileHasNoDependency(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.Write([]byte("v2")); err != nil {
		t.Fatalf("write: %v", err)
	}

	needs := crash.Needs(rec)
	if len(needs) < 2 {
		t.Fatalf("got %d entries, want at least 2", len(needs))
	}
	if len(needs[1]) != 0 {
		t.Errorf("the write needs %v, want none — the file predates the record", needs[1])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestAWriteNeeds|TestARenameCarries|TestAPreExisting' -v`
Expected: FAIL to build with `undefined: crash.Needs`.

- [ ] **Step 3: Write minimal implementation**

Add to `crash/export_test.go`:

```go
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
```

The recording logic from Task 2 already sets `needs`. If `TestAPreExistingFileHasNoDependency` fails, the cause is `OpenFile`'s `existed` guess: a file opened without `O_CREATE` must not enter `origin`. Fix by making `origin` record only paths this run created:

```go
	switch {
	case flag&os.O_CREATE != 0 && !r.existedBefore(p):
		n := r.add(entry{k: kCreate, path: p})
		r.origin[p] = n
	default:
		r.add(entry{k: kOpen, path: p})
	}
```

with

```go
// existedBefore reports whether p was present when Record was called. Task 4
// answers it from the snapshot; until then, the base is asked directly, which
// is bookkeeping and therefore takes no index.
func (r *Recorder) existedBefore(p string) bool {
	_, err := r.base.Stat(filepath.Join(r.root, filepath.FromSlash(p)))
	return err == nil
}
```

**Two ordering rules, both of which cost a review round when they were missing.**

1. `existedBefore` must be called BEFORE `r.base.OpenFile`, never after. After the
   base call, `O_CREATE` has already created the file, so the check always
   reports "existed" and no create entry is ever recorded. Calling it before is
   the only placement that answers the question actually being asked.
2. `r.mu` must be held across `existedBefore`, the base call, AND the entry, as
   a single critical section — the same rule Task 2's `Read` and `Write` follow.
   Checking outside the lock and acting inside it lets two goroutines opening
   the same new path both decide it did not exist, and both record a create.

**A test must pin rule 1.** The three tests in this task all pass against the
wrong placement, so none of them covers it. Open a file with `O_CREATE` that
already existed when `Record` was called, and assert that NO create entry is
recorded for it and that a later write into it therefore has empty `needs`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS, all tests from Tasks 1-3.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/record.go crash/export_test.go crash/needs_test.go
git commit -m "feat(crash): the dependency that makes a candidate state legal"
```

---

### Task 4: The snapshot, and comparing two trees

**Files:**
- Create: `crash/snapshot.go`
- Modify: `crash/record.go` — `Record` takes the snapshot, `existedBefore` reads it
- Test: `crash/snapshot_test.go`

**Interfaces:**
- Consumes: nothing outside the standard library.
- Produces:
  - `type node struct { dir bool; data []byte; mode os.FileMode }`
  - `type tree map[string]node` — keys are slash-separated relative paths
  - `func readTree(root string) (tree, error)`
  - `func (t tree) clone() tree`
  - `func (t tree) writeTo(dir string) error`
  - `func diffTree(want, got tree) string` — empty when equal

- [ ] **Step 1: Write the failing test**

```go
package crash

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTreeThenWriteToRoundTrips(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readTree(src)
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}

	dst := t.TempDir()
	if err := got.writeTo(dst); err != nil {
		t.Fatalf("writeTo: %v", err)
	}
	back, err := readTree(dst)
	if err != nil {
		t.Fatalf("readTree of the copy: %v", err)
	}
	if d := diffTree(got, back); d != "" {
		t.Errorf("a tree did not survive a round trip:\n%s", d)
	}
}

// diffTree is the instrument the positive control depends on. If it cannot
// report a difference, the control passes against a broken replay and every
// finding after it is a fiction.
func TestDiffTreeReportsEachKindOfDifference(t *testing.T) {
	base := tree{"a": {data: []byte("x")}}

	cases := []struct {
		name string
		got  tree
	}{
		{"a missing file", tree{}},
		{"an extra file", tree{"a": {data: []byte("x")}, "b": {data: []byte("y")}}},
		{"different contents", tree{"a": {data: []byte("z")}}},
		{"a file that became a directory", tree{"a": {dir: true}}},
	}
	for _, c := range cases {
		if diffTree(base, c.got) == "" {
			t.Errorf("%s: diffTree reported no difference, so it cannot report the negative", c.name)
		}
	}

	if d := diffTree(base, tree{"a": {data: []byte("x")}}); d != "" {
		t.Errorf("two equal trees differ: %s", d)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestReadTree|TestDiffTree' -v`
Expected: FAIL to build with `undefined: readTree`.

- [ ] **Step 3: Write minimal implementation**

```go
package crash

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// node is one name in a tree. A directory carries no data.
type node struct {
	dir  bool
	data []byte
	mode os.FileMode
}

// tree is a directory's whole content, keyed by slash-separated relative path.
// The root itself is not a key.
type tree map[string]node

func readTree(root string) (tree, error) {
	t := tree{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		key := filepath.ToSlash(rel)
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if d.IsDir() {
			t[key] = node{dir: true, mode: info.Mode().Perm()}
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		t[key] = node{data: data, mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (t tree) clone() tree {
	out := make(tree, len(t))
	for k, v := range t {
		data := append([]byte(nil), v.data...)
		out[k] = node{dir: v.dir, data: data, mode: v.mode}
	}
	return out
}

// writeTo materialises the tree under dir. Directories are created before the
// files inside them, which sorting the keys guarantees.
func (t tree) writeTo(dir string) error {
	keys := t.keys()
	for _, k := range keys {
		n := t[k]
		p := filepath.Join(dir, filepath.FromSlash(k))
		if n.dir {
			if err := os.MkdirAll(p, perm(n.mode, 0o700)); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, n.data, perm(n.mode, 0o600)); err != nil {
			return err
		}
	}
	return nil
}

// perm supplies a default when a node carries no mode, which happens for a
// tree built by the replay rather than read from a disk.
func perm(m, def os.FileMode) os.FileMode {
	if m == 0 {
		return def
	}
	return m
}

// keys returns the paths in a stable order. Go randomises map iteration on
// purpose, so anything that reaches a state name or a comparison must sort.
func (t tree) keys() []string {
	out := make([]string, 0, len(t))
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diffTree describes how got differs from want, and returns "" when they are
// identical. It must be able to report every kind of difference, because the
// positive control believes it.
func diffTree(want, got tree) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, k := range want.keys() {
		seen[k] = true
		w := want[k]
		g, ok := got[k]
		switch {
		case !ok:
			fmt.Fprintf(&b, "missing: %s\n", k)
		case w.dir != g.dir:
			fmt.Fprintf(&b, "kind differs: %s (want dir=%v, got dir=%v)\n", k, w.dir, g.dir)
		case !w.dir && !bytes.Equal(w.data, g.data):
			fmt.Fprintf(&b, "contents differ: %s (want %d bytes, got %d bytes)\n", k, len(w.data), len(g.data))
		}
	}
	for _, k := range got.keys() {
		if !seen[k] {
			fmt.Fprintf(&b, "unexpected: %s\n", k)
		}
	}
	return b.String()
}
```

Then change `Record` to take the snapshot, and `existedBefore` to read it:

```go
func Record(base faultfs.FS, root string) *Recorder {
	r := &Recorder{base: base, root: root, origin: map[string]int{}}
	snap, err := readTree(root)
	if err != nil {
		r.fail(fmt.Errorf("crash: cannot snapshot the record root %q: %w", root, err))
		snap = tree{}
	}
	r.snap = snap
	return r
}

func (r *Recorder) exists(p string) bool {
	return r.live[p]
}
```

**Not `existedBefore`, and not the snapshot.** The brief for Task 3 asked "was p
present when `Record` was called", answered from `r.snap`. `OpenFile` does not
need that question. It needs "does p exist RIGHT NOW", so that an `O_CREATE`
open records a create only when it actually creates something.

The two agree at the start of a run and diverge the moment the run changes the
namespace, in two ways that both matter:

- Two concurrent opens on one new path. The first records a create; the second
  asks the snapshot, does not find the path, and records a second one.
- A snapshot path that is removed and then re-created. It is still in `snap`, so
  no create entry is recorded for the re-creation, the later write carries no
  dependency, and the replay fails with "writes to a path no present entry
  created".

So the `Recorder` carries a `live map[string]bool`, seeded at `Record` from the
snapshot's keys and updated at all five sites that change the namespace: create
and mkdir add, remove deletes, and rename deletes the old name and adds the new.
`origin` stays as it is — it answers a different question, which entry index made
a path exist — and the two must be updated together or they drift.

Add the `snap tree` field to `Recorder`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS, all tests so far.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/snapshot.go crash/snapshot_test.go crash/record.go
git commit -m "feat(crash): the snapshot the replay starts from, and a diff that can report the negative"
```

---

### Task 5: The replay

**Files:**
- Create: `crash/rebuild.go`
- Test: `crash/rebuild_test.go`

**Interfaces:**
- Consumes: `tree`, `node`, `entry`, `kind` from Tasks 2 and 4.
- Produces: `func replay(snap tree, entries []entry, present map[int]bool, lost map[int][]unit) (tree, error)` — `present` names the entry indexes that reached the disk, and `lost` names the byte ranges of a write that did not. An entry absent from `present` did not happen at all. A range in `lost` leaves whatever the replay already had at those bytes, which is the truthful model and NOT zeroes.

- [ ] **Step 1: Write the failing test**

```go
package crash

import (
	"testing"
)

func TestReplayAppliesEachKindOfEntry(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kWrite, path: "tmp", off: 0, data: []byte("hello")},
		{n: 3, k: kRename, path: "tmp", to: "data"},
		{n: 4, k: kMkdir, path: "d"},
		{n: 5, k: kTruncate, path: "data", size: 2},
	}
	all := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true}

	got, err := replay(tree{}, entries, all, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	want := tree{
		"data": {data: []byte("he")},
		"d":    {dir: true},
	}
	if d := diffTree(want, got); d != "" {
		t.Errorf("replay produced the wrong tree:\n%s", d)
	}
}

// A write past the end of a file zero-fills the gap, which is what a real
// filesystem does. Getting this wrong makes the positive control fail on any
// scenario that seeks, and it would be blamed on the scenario.
func TestReplayZeroFillsAGap(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", off: 3, data: []byte("z")},
	}
	got, err := replay(tree{}, entries, map[int]bool{1: true, 2: true}, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := tree{"a": {data: []byte{0, 0, 0, 'z'}}}
	if d := diffTree(want, got); d != "" {
		t.Errorf("a gap was not zero-filled:\n%s", d)
	}
}

// An entry that is absent did not happen, and the replay must not apply it.
func TestReplaySkipsAnAbsentEntry(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", off: 0, data: []byte("hello")},
	}
	got, err := replay(tree{}, entries, map[int]bool{1: true}, nil)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	want := tree{"a": {data: nil}}
	if d := diffTree(want, got); d != "" {
		t.Errorf("an absent write was applied:\n%s", d)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run TestReplay -v`
Expected: FAIL to build with `undefined: replay`.

- [ ] **Step 3: Write minimal implementation**

```go
package crash

import "fmt"

// replay applies the entries named by present, in log order, to a copy of the
// snapshot, and returns the resulting tree.
//
// An entry absent from present did not reach the disk. The caller has already
// applied the dependency closure, so an absent create cannot be followed by a
// present write into the same name -- but replay still refuses that pairing
// rather than inventing a file, because a silent invention here would make the
// generator's defect look like the store's.
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
			// Only the ranges that reached the disk are applied. A lost range
			// is NOT zeroed: it keeps whatever the replay already had there,
			// which for a backpatched header is the placeholder the same run
			// wrote earlier. Zeroing would invent a state the store can never
			// see -- graphdb's SSTable header zeroes to IndexOffset = 0, which
			// is structurally valid, so the reader would parse the body as an
			// index and fail in a way it never fails in reality.
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

// presentRanges returns the complement of the lost ranges within [0, size), in
// order. It is what turns "these sectors did not land" into "these sectors
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

// writeAt places data at off, growing the file and zero-filling any gap, which
// is what a real filesystem does for a write past the end.
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

func resize(dst []byte, size int64) []byte {
	if int64(len(dst)) >= size {
		return append([]byte(nil), dst[:size]...)
	}
	grown := make([]byte, size)
	copy(grown, dst)
	return grown
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/rebuild.go crash/rebuild_test.go
git commit -m "feat(crash): the replay, which refuses to invent a file it was not given"
```

---

### Task 6: The positive control, and its own control

**Files:**
- Create: `crash/control.go`
- Test: `crash/control_test.go`

**Interfaces:**
- Consumes: `replay`, `readTree`, `diffTree`, `Recorder`.
- Produces: `func (r *Recorder) checkReplay() error` — nil when replaying the whole record reproduces the base directory.

- [ ] **Step 1: Write the failing test**

```go
package crash

import (
	"os"
	"path/filepath"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

func TestTheControlPassesForATruthfulRecord(t *testing.T) {
	dir := t.TempDir()
	r := Record(faultfs.OS(), dir)

	f, err := r.OpenFile(filepath.Join(dir, "tmp"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Rename(filepath.Join(dir, "tmp"), filepath.Join(dir, "data")); err != nil {
		t.Fatal(err)
	}

	if err := r.checkReplay(); err != nil {
		t.Errorf("the control failed on a truthful record: %v", err)
	}
}

// The control needs its own control. A watcher that cannot report the negative
// reports nothing, and this repository has already paid for that once.
func TestTheControlFailsWhenTheReplayIsWrong(t *testing.T) {
	dir := t.TempDir()
	r := Record(faultfs.OS(), dir)

	f, err := r.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Damage the record the way a broken replay would: drop the last byte.
	// The control must notice, or it is decoration.
	for i := range r.entries {
		if r.entries[i].k == kWrite {
			r.entries[i].data = r.entries[i].data[:len(r.entries[i].data)-1]
		}
	}

	if err := r.checkReplay(); err == nil {
		t.Fatal("the control passed against a damaged record, so it can never fail")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run TestTheControl -v`
Expected: FAIL to build with `r.checkReplay undefined`.

- [ ] **Step 3: Write minimal implementation**

```go
package crash

import "fmt"

// checkReplay is this package's positive control, and Run calls it before it
// builds any candidate state.
//
// Replaying the WHOLE record must reproduce the directory the scenario
// actually wrote -- every name, every size, every byte. If it does not, the
// replay is wrong, every state built afterwards is a fiction, and every
// finding is noise. The operation log was chosen over a snapshot at each sync
// precisely because its one weakness converts into this assertion, which runs
// on every scenario a user ever writes rather than on the fixtures we thought
// of.
func (r *Recorder) checkReplay() error {
	r.mu.Lock()
	entries := append([]entry(nil), r.entries...)
	snap := r.snap
	root := r.root
	r.mu.Unlock()

	present := make(map[int]bool, len(entries))
	for _, e := range entries {
		present[e.n] = true
	}

	got, err := replay(snap, entries, present, nil)
	if err != nil {
		return fmt.Errorf("crash: the replay of the whole record failed, so no state it builds can be trusted: %w", err)
	}
	want, err := readTree(root)
	if err != nil {
		return fmt.Errorf("crash: cannot read the recorded root %q to check the replay: %w", root, err)
	}
	if d := diffTree(want, got); d != "" {
		return fmt.Errorf("crash: replaying the whole record did not reproduce the directory the scenario wrote, so the replay is wrong and every state is a fiction:\n%s", d)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS. If `TestTheControlPassesForATruthfulRecord` fails, the replay or the recorder is wrong — that is the control doing its job, and the fix belongs in Task 2 or Task 5, not here.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/control.go crash/control_test.go
git commit -m "feat(crash): the control that proves the replay, and a test that proves the control"
```

---

### Task 7: The durable and pending split

**Files:**
- Create: `crash/state.go`
- Test: `crash/durable_test.go`

**Interfaces:**
- Consumes: `entry`, `kind`, `Model`.
- Produces:
  - `func crashPoints(entries []entry) []int` — the indexes after which a crash point sits
  - `func split(entries []entry, k int, m Model) (durable, pending []int)`

- [ ] **Step 1: Write the failing test**

```go
package crash

import (
	"reflect"
	"testing"
)

func TestOnlyAMutationCarriesACrashPoint(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kRead, path: "a"},
		{n: 3, k: kWrite, path: "a"},
		{n: 4, k: kSync, path: "a"},
		{n: 5, k: kOpen, path: "a"},
	}
	got := crashPoints(entries)
	want := []int{1, 3}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("crashPoints = %v, want %v — a read, a sync and a plain open change nothing", got, want)
	}
}

func TestAFileSyncMakesThatFilesWritesDurable(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a"},
		{n: 3, k: kSync, path: "a"},
		{n: 4, k: kWrite, path: "a"},
	}
	durable, pending := split(entries, 4, Model{})
	if !contains(durable, 2) {
		t.Errorf("write 2 is %v/%v, want durable — a sync on its own file followed it", durable, pending)
	}
	if !contains(pending, 4) {
		t.Errorf("write 4 is %v/%v, want pending — no sync followed it", durable, pending)
	}
}

// A sync on a file does NOT make a rename durable. That is the missing-parent-
// fsync defect, and it is the most common real one.
func TestAFileSyncDoesNotMakeARenameDurable(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kWrite, path: "tmp"},
		{n: 3, k: kSync, path: "tmp"},
		{n: 4, k: kRename, path: "tmp", to: "data"},
		{n: 5, k: kSync, path: "tmp"},
	}
	durable, pending := split(entries, 4, Model{})
	if !contains(pending, 4) {
		t.Errorf("the rename is %v/%v, want pending — only a directory sync makes it durable", durable, pending)
	}
}

func TestADirectorySyncMakesMetadataDurable(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kRename, path: "tmp", to: "data"},
		{n: 3, k: kSync, path: ".", dir: true},
	}
	durable, pending := split(entries, 2, Model{})
	_ = pending
	if !contains(durable, 2) {
		t.Errorf("the rename is not durable after a directory sync: durable=%v", durable)
	}
}

// MetadataDurable is the Windows switch, and this is the only test that can
// catch it being read and then ignored.
func TestMetadataDurableMakesARenameDurableAtOnce(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kRename, path: "tmp", to: "data"},
	}
	durable, _ := split(entries, 2, Model{MetadataDurable: true})
	if !contains(durable, 2) {
		t.Errorf("with MetadataDurable the rename is not durable: durable=%v", durable)
	}
	strict, _ := split(entries, 2, Model{})
	if contains(strict, 2) {
		t.Errorf("without MetadataDurable the rename is durable, so the field does nothing")
	}
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestOnlyAMutation|TestAFileSync|TestADirectorySync|TestMetadataDurable' -v`
Expected: FAIL to build with `undefined: crashPoints`, `undefined: split`.

- [ ] **Step 3: Write minimal implementation**

```go
package crash

import "path"

// crashPoints returns the entry indexes after which a crash point sits.
//
// A read, a sync and a plain open change nothing, so a crash after one of them
// leaves the same state as a crash after the previous mutation. Visiting both
// would double the walk and prove nothing new.
func crashPoints(entries []entry) []int {
	var out []int
	for _, e := range entries {
		if e.k.mutates() {
			out = append(out, e.n)
		}
	}
	return out
}

// split partitions the entries at or before k into those a crash would keep
// and those it may lose.
//
// A write or a truncate is covered by a sync on its own file handle. A create,
// rename, remove or mkdir is covered by a sync on a handle opened on the
// directory that holds the name. Close covers nothing, because POSIX does not
// make close(2) flush.
func split(entries []entry, k int, m Model) (durable, pending []int) {
	// syncedAfter[i] answers: does a covering sync appear after entry i and at
	// or before k? Walking backwards answers every entry in one pass.
	fileSynced := map[string]bool{}
	dirSynced := map[string]bool{}
	isDurable := map[int]bool{}

	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.n > k {
			continue
		}
		switch {
		case e.k == kSync && e.dir:
			dirSynced[e.path] = true
			continue
		case e.k == kSync:
			fileSynced[e.path] = true
			continue
		case !e.k.mutates():
			continue
		}

		switch e.k {
		case kWrite, kTruncate:
			isDurable[e.n] = fileSynced[e.path]
		default:
			// Metadata. The directory that holds the name is the one that must
			// be synced. A rename touches two names, so both directories must
			// be covered for it to be durable.
			isDurable[e.n] = m.MetadataDurable ||
				(dirSynced[path.Dir(e.path)] && (e.to == "" || dirSynced[path.Dir(e.to)]))
		}
	}

	for _, e := range entries {
		if e.n > k || !e.k.mutates() {
			continue
		}
		if isDurable[e.n] {
			durable = append(durable, e.n)
			continue
		}
		pending = append(pending, e.n)
	}
	return durable, pending
}
```

Note for the implementer: `path.Dir("tmp")` returns `"."`, so a sync of the record root must be recorded with path `"."`. The recorder's `rel` already produces `"."` for the root itself.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/state.go crash/durable_test.go
git commit -m "feat(crash): which changes a crash keeps, and which it may lose"
```

---

### Task 8: Loss units

**Files:**
- Modify: `crash/state.go`
- Test: `crash/units_test.go`

**Interfaces:**
- Consumes: `entry`, `Model`, `split`.
- Produces:
  - `type unit struct { entry int; from, to int64 }` — `from`/`to` are byte offsets within the entry's data; both zero means the whole entry
  - `func units(entries []entry, pending []int, k int, m Model) []unit`

- [ ] **Step 1: Write the failing test**

```go
package crash

import "testing"

func TestSectorZeroLosesAWholeWriteCall(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", data: make([]byte, 10000)}}
	got := units(entries, []int{1}, 1, Model{Sector: 0})
	if len(got) != 1 {
		t.Fatalf("got %d units, want 1 for a whole call", len(got))
	}
}

func TestASectorSplitsAWriteAtItsBoundaries(t *testing.T) {
	// One write of 10000 bytes at offset 0, sector 4096, gives 3 units:
	// [0,4096) [4096,8192) [8192,10000).
	entries := []entry{{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 10000)}}
	got := units(entries, []int{1}, 1, Model{Sector: 4096})
	if len(got) != 3 {
		t.Fatalf("got %d units, want 3", len(got))
	}
	if got[0].from != 0 || got[0].to != 4096 {
		t.Errorf("unit 0 = [%d,%d), want [0,4096)", got[0].from, got[0].to)
	}
	if got[2].from != 8192 || got[2].to != 10000 {
		t.Errorf("unit 2 = [%d,%d), want [8192,10000)", got[2].from, got[2].to)
	}
}

// A metadata entry never splits, whatever the sector size. No filesystem
// half-renames a file.
func TestMetadataNeverSplits(t *testing.T) {
	entries := []entry{{n: 1, k: kRename, path: "a", to: "b"}}
	got := units(entries, []int{1}, 1, Model{Sector: 512})
	if len(got) != 1 {
		t.Fatalf("got %d units for a rename, want 1", len(got))
	}
	if got[0].from != 0 || got[0].to != 0 {
		t.Errorf("a rename unit carries a byte range [%d,%d), want none", got[0].from, got[0].to)
	}
}

// SplitAtCrashOnly keeps the part-written record without paying for it at
// every earlier write.
func TestSplitAtCrashOnlyOnlySplitsTheWriteAtTheCrash(t *testing.T) {
	entries := []entry{
		{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 10000)},
		{n: 2, k: kWrite, path: "a", off: 10000, data: make([]byte, 10000)},
	}
	got := units(entries, []int{1, 2}, 2, Model{Sector: 4096, SplitAtCrashOnly: true})
	// entry 1 whole (1 unit) + entry 2 split (3 units) = 4
	if len(got) != 4 {
		t.Fatalf("got %d units, want 4 — only the write at the crash point splits", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestSectorZero|TestASectorSplits|TestMetadataNever|TestSplitAtCrashOnly' -v`
Expected: FAIL to build with `undefined: units`.

- [ ] **Step 3: Write minimal implementation**

Append to `crash/state.go`:

```go
// unit is the smallest thing a candidate state can lose. from and to are byte
// offsets within the entry's data; both zero means the whole entry.
type unit struct {
	entry int
	from  int64
	to    int64
	// sect is which sector of the write this is, from 0. It is carried rather
	// than derived, because the last sector of a write is usually short and
	// from/(to-from) then names it wrongly: a 10000 byte write at sector 4096
	// ends with from=8192, to=10000, and 8192/1808 is 4 rather than 2.
	sect int
}

// whole reports whether this unit covers its entry entirely.
func (u unit) whole() bool { return u.from == 0 && u.to == 0 }

// units splits the pending entries into the things a crash can lose.
//
// A metadata entry is never split, because no filesystem half-renames a file.
// A write splits at sector boundaries of its ABSOLUTE offset range, so two
// writes to the same sector of the same file split at the same place.
func units(entries []entry, pending []int, k int, m Model) []unit {
	byIndex := make(map[int]entry, len(entries))
	for _, e := range entries {
		byIndex[e.n] = e
	}

	var out []unit
	for _, n := range pending {
		e := byIndex[n]
		splittable := e.k == kWrite && m.Sector > 0 && len(e.data) > 0
		if splittable && m.SplitAtCrashOnly && n != k {
			splittable = false
		}
		if !splittable {
			out = append(out, unit{entry: n})
			continue
		}
		out = append(out, sectorsOf(e, int64(m.Sector))...)
	}
	return out
}

// sectorsOf cuts one write at the sector boundaries its absolute offset falls
// on. The first and last pieces are usually short, which is correct: a write
// that starts mid-sector shares that sector with whatever was there before.
func sectorsOf(e entry, sector int64) []unit {
	start := e.off
	end := e.off + int64(len(e.data))

	var out []unit
	for at, i := start, 0; at < end; i++ {
		next := (at/sector + 1) * sector
		if next > end {
			next = end
		}
		out = append(out, unit{entry: e.n, from: at - start, to: next - start, sect: i})
		at = next
	}
	return out
}
```

Note for the implementer: the test asserts `from`/`to` relative to the entry's data, and `sectorsOf` returns them that way. Keep it relative, because `replay` slices `e.data`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/state.go crash/units_test.go
git commit -m "feat(crash): the unit a crash can lose, and the rename that cannot be halved"
```

---

### Task 9: The closure, and removing duplicate states

**Files:**
- Modify: `crash/state.go`
- Test: `crash/closure_test.go`

**Interfaces:**
- Consumes: `entry`, `unit`.
- Produces:
  - `func closure(entries []entry, keep map[int]bool) map[int]bool` — given the entry indexes a state keeps, drop every entry that needs a dropped one
  - `func fingerprint(t tree) string` — a stable hash of a tree, for deduplication

- [ ] **Step 1: Write the failing test**

```go
package crash

import "testing"

// Dropping a create must drop every write into it. A file with data and no
// directory entry is a state no power cut can make, and a finding against one
// is noise.
func TestDroppingACreateDropsItsWrites(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "a"},
		{n: 2, k: kWrite, path: "a", needs: []int{1}},
		{n: 3, k: kWrite, path: "a", needs: []int{1}},
	}
	keep := closure(entries, map[int]bool{2: true, 3: true}) // create dropped

	if keep[2] || keep[3] {
		t.Errorf("keep = %v, want the writes dropped with their create", keep)
	}
}

// The closure is transitive: a rename that needs a dropped create is dropped,
// and a write into the renamed name goes with it.
func TestTheClosureIsTransitive(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "tmp"},
		{n: 2, k: kRename, path: "tmp", to: "data", needs: []int{1}},
		{n: 3, k: kWrite, path: "data", needs: []int{2}},
	}
	keep := closure(entries, map[int]bool{2: true, 3: true})
	if keep[2] || keep[3] {
		t.Errorf("keep = %v, want both dropped through the rename", keep)
	}
}

func TestFingerprintIsStableAndDiscriminating(t *testing.T) {
	a := tree{"x": {data: []byte("1")}, "y": {dir: true}}
	b := tree{"y": {dir: true}, "x": {data: []byte("1")}}
	c := tree{"x": {data: []byte("2")}, "y": {dir: true}}

	if fingerprint(a) != fingerprint(b) {
		t.Error("two equal trees fingerprint differently, so deduplication depends on map order")
	}
	if fingerprint(a) == fingerprint(c) {
		t.Error("two different trees fingerprint the same, so distinct states would be merged")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestDroppingACreate|TestTheClosureIs|TestFingerprint' -v`
Expected: FAIL to build with `undefined: closure`, `undefined: fingerprint`.

- [ ] **Step 3: Write minimal implementation**

Append to `crash/state.go`:

```go
import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// closure drops every entry that needs a dropped one, repeatedly, until
// nothing more falls.
//
// Without it the generator produces states that no power cut can make -- a
// file holding data with no directory entry, most often. Those are not
// findings. They train a reader to ignore the tool, which is worse than having
// no tool.
func closure(entries []entry, keep map[int]bool) map[int]bool {
	out := make(map[int]bool, len(keep))
	for k, v := range keep {
		out[k] = v
	}
	for changed := true; changed; {
		changed = false
		for _, e := range entries {
			if !out[e.n] {
				continue
			}
			for _, need := range e.needs {
				if !out[need] {
					out[e.n] = false
					changed = true
					break
				}
			}
		}
	}
	return out
}

// fingerprint is a stable hash of a tree, used to drop duplicate states before
// any of them becomes a subtest.
//
// The keys are sorted, because Go randomises map iteration on purpose. A
// fingerprint that depended on that order would merge different states on one
// run and split identical ones on the next.
func fingerprint(t tree) string {
	h := sha256.New()
	for _, k := range t.keys() {
		n := t[k]
		fmt.Fprintf(h, "%s\x00%v\x00%d\x00", k, n.dir, len(n.data))
		h.Write(n.data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/state.go crash/closure_test.go
git commit -m "feat(crash): the closure that keeps a state legal, and a fingerprint that ignores map order"
```

---

### Task 10: Cover enumeration and the cap

**Files:**
- Modify: `crash/state.go`
- Test: `crash/cover_test.go`

**Interfaces:**
- Consumes: `unit`, `Cover`, `Exhaustive`, `Prefixes`.
- Produces:
  - `const maxStates = 4096` — provisional, measured in Task 15
  - `var errTooManyStates = errors.New(...)`
  - `func subsets(u []unit, c Cover) ([][]unit, error)` — each result is the set of units that were **lost**

- [ ] **Step 1: Write the failing test**

```go
package crash

import (
	"errors"
	"testing"
)

func TestExhaustiveVisitsEverySubset(t *testing.T) {
	u := []unit{{entry: 1}, {entry: 2}, {entry: 3}}
	got, err := subsets(u, Exhaustive)
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}
	if len(got) != 8 {
		t.Errorf("got %d subsets, want 8 = 2^3", len(got))
	}
}

func TestPrefixesVisitsOnePerBoundary(t *testing.T) {
	u := []unit{{entry: 1}, {entry: 2}, {entry: 3}}
	got, err := subsets(u, Prefixes)
	if err != nil {
		t.Fatalf("subsets: %v", err)
	}
	if len(got) != 4 {
		t.Errorf("got %d subsets, want 4 = u+1", len(got))
	}
}

// Prefixes must be a subset of Exhaustive for the same units, or the two are
// not two covers of one space.
func TestPrefixesAreASubsetOfExhaustive(t *testing.T) {
	u := []unit{{entry: 1}, {entry: 2}, {entry: 3}}
	all, _ := subsets(u, Exhaustive)
	pre, _ := subsets(u, Prefixes)

	seen := map[string]bool{}
	for _, s := range all {
		seen[keyOf(s)] = true
	}
	for _, s := range pre {
		if !seen[keyOf(s)] {
			t.Errorf("prefix state %v is not among the exhaustive states", s)
		}
	}
}

// Passing the cap fails, and it must not quietly visit a subset: a partial
// walk reports a pass the same way a complete one does.
func TestExhaustiveRefusesPastTheCap(t *testing.T) {
	u := make([]unit, 40) // 2^40 states
	for i := range u {
		u[i] = unit{entry: i + 1}
	}
	_, err := subsets(u, Exhaustive)
	if !errors.Is(err, errTooManyStates) {
		t.Fatalf("err = %v, want errTooManyStates", err)
	}
}

func keyOf(us []unit) string {
	s := ""
	for _, u := range us {
		s += string(rune('a'+u.entry)) + ":"
	}
	return s
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestExhaustive|TestPrefixes' -v`
Expected: FAIL to build with `undefined: subsets`.

- [ ] **Step 3: Write minimal implementation**

Append to `crash/state.go`:

```go
// maxStates bounds one crash point's exhaustive walk. It is provisional; Task
// 15 replaces it with a number measured from the reference stores.
const maxStates = 4096

var errTooManyStates = errors.New("crash: too many states for an exhaustive cover")

// subsets returns the sets of units a crash may lose, by the cover rule. Each
// result is the LOST set, so the empty set is the state in which everything
// pending reached the disk.
func subsets(u []unit, c Cover) ([][]unit, error) {
	if c == Prefixes {
		// The pending units persist in log order, so what is lost is a suffix.
		out := make([][]unit, 0, len(u)+1)
		for i := len(u); i >= 0; i-- {
			out = append(out, append([]unit(nil), u[i:]...))
		}
		return out, nil
	}

	if len(u) > 20 || 1<<uint(len(u)) > maxStates {
		return nil, fmt.Errorf("%w: %d pending units give %s states, and the cap is %d. Use Prefixes, or sync more often in the scenario",
			errTooManyStates, len(u), countOf(len(u)), maxStates)
	}

	total := 1 << uint(len(u))
	out := make([][]unit, 0, total)
	for mask := 0; mask < total; mask++ {
		var lost []unit
		for i, unit := range u {
			if mask&(1<<uint(i)) != 0 {
				lost = append(lost, unit)
			}
		}
		out = append(out, lost)
	}
	return out, nil
}

// countOf renders 2^n without overflowing for a large n, so the diagnostic can
// state the real size rather than a wrapped one.
func countOf(n int) string {
	if n >= 63 {
		return fmt.Sprintf("2^%d", n)
	}
	return fmt.Sprintf("%d", int64(1)<<uint(n))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/state.go crash/cover_test.go
git commit -m "feat(crash): two covers of one space, and a cap that refuses rather than truncates"
```

---

### Task 11: Naming a state

**Files:**
- Create: `crash/name.go`
- Test: `crash/name_test.go`

**Interfaces:**
- Consumes: `entry`, `unit`.
- Produces:
  - `const maxNameLen = 80` — provisional, measured in Task 15
  - `func unitName(byIndex map[int]entry, u unit) string`
  - `func stateName(byIndex map[int]entry, lost []unit) string`
  - `func pointName(byIndex map[int]entry, k int) string`
  - `func occurrences(entries []entry) map[int]int` — entry index to which occurrence of that op on that path it is

- [ ] **Step 1: Write the failing test**

```go
package crash

import (
	"strings"
	"testing"
)

// A unit is named structurally, not by ordinal. role.Key exists because an
// ordinal means nothing outside the run that produced it, and a subtest name
// has the same duty plus a harder one: go test -run must match it later.
func TestAUnitIsNamedByFileOperationAndOccurrence(t *testing.T) {
	entries := []entry{
		{n: 1, k: kCreate, path: "data.tmp"},
		{n: 2, k: kWrite, path: "data.tmp"},
		{n: 3, k: kWrite, path: "data.tmp"},
	}
	byIndex := index(entries)

	if got := unitName(byIndex, unit{entry: 3}); got != "data.tmp:write2" {
		t.Errorf("unitName = %q, want %q", got, "data.tmp:write2")
	}
}

func TestASectorUnitCarriesItsIndex(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "a", off: 0, data: make([]byte, 8192)}}
	byIndex := index(entries)

	got := unitName(byIndex, unit{entry: 1, from: 4096, to: 8192, sect: 1})
	if !strings.HasSuffix(got, ".s1") {
		t.Errorf("unitName = %q, want a .s1 suffix for the second sector", got)
	}
}

// A slash would create a third level of subtest nesting and split one state's
// name across two levels.
func TestAStateNameCarriesNoSlash(t *testing.T) {
	entries := []entry{{n: 1, k: kWrite, path: "sub/dir/a"}}
	byIndex := index(entries)

	if got := stateName(byIndex, []unit{{entry: 1}}); strings.Contains(got, "/") {
		t.Errorf("stateName = %q, which contains a slash and would nest", got)
	}
}

func TestAnEmptyLostSetIsNamedNone(t *testing.T) {
	if got := stateName(map[int]entry{}, nil); got != "lost=none" {
		t.Errorf("stateName = %q, want %q", got, "lost=none")
	}
}

// A long name becomes a count and a hash, so it stays matchable by -run. The
// detail belongs in the failure message either way.
func TestALongNameBecomesACountAndAHash(t *testing.T) {
	var entries []entry
	var lost []unit
	for i := 1; i <= 30; i++ {
		entries = append(entries, entry{n: i, k: kWrite, path: "averylongfilename"})
		lost = append(lost, unit{entry: i})
	}
	got := stateName(index(entries), lost)

	if len(got) > maxNameLen {
		t.Errorf("stateName is %d characters, want at most %d", len(got), maxNameLen)
	}
	if !strings.Contains(got, "30units:") {
		t.Errorf("stateName = %q, want it to carry the count", got)
	}
}

// The same lost set must produce the same name on every run, or -run cannot
// re-run a failure.
func TestAStateNameIsStable(t *testing.T) {
	entries := []entry{
		{n: 1, k: kWrite, path: "a"},
		{n: 2, k: kWrite, path: "b"},
	}
	byIndex := index(entries)
	lost := []unit{{entry: 2}, {entry: 1}}

	first := stateName(byIndex, lost)
	for i := 0; i < 50; i++ {
		if got := stateName(byIndex, lost); got != first {
			t.Fatalf("stateName changed between runs: %q then %q", first, got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestAUnitIs|TestASector|TestAState|TestAnEmpty|TestALong' -v`
Expected: FAIL to build with `undefined: unitName`.

- [ ] **Step 3: Write minimal implementation**

```go
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

var opNames = map[kind]string{
	kCreate: "create", kWrite: "write", kTruncate: "truncate",
	kRename: "rename", kRemove: "remove", kMkdir: "mkdir", kSync: "sync",
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
// naming one unit in a failure message has no reason to hold it.
func unitName(byIndex map[int]entry, u unit) string {
	e, ok := byIndex[u.entry]
	if !ok {
		return fmt.Sprintf("entry%d", u.entry)
	}

	var all []entry
	for _, v := range byIndex {
		all = append(all, v)
	}
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
// slash would create another level of nesting, and a space becomes an
// underscore in a Go test name anyway, so it is done here where it is visible.
func safe(p string) string {
	p = strings.ReplaceAll(p, "/", "|")
	return strings.ReplaceAll(p, " ", "_")
}

// stateName names the set of units a state lost.
//
// When the full list would be too long to read, the name becomes a count and a
// stable hash of the structural keys. A name a person cannot read is still a
// name go test -run can match, and the detail belongs in the failure message
// either way.
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

	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("lost=%dunits:%s", len(lost), hex.EncodeToString(h[:])[:8])
}

// pointName names the crash point: the operation the process died after.
func pointName(byIndex map[int]entry, k int) string {
	return "after=" + unitName(byIndex, unit{entry: k})
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/name.go crash/name_test.go
git commit -m "feat(crash): a state name that go test -run can match, and that an extra write does not move"
```

---

### Task 12: `Run`, the refusals, and the subtest walk

**Files:**
- Create: `crash/run.go`
- Modify: `crash/export_test.go` — expose `plan`
- Test: `crash/run_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1-11.
- Produces:
  - `type state struct { point int; lost []unit; name string; result tree }`
  - `func plan(r *Recorder, m Model) ([]state, error)` — the internal that returns a diagnostic instead of failing a test
  - `func Run(t *testing.T, rec *Recorder, m Model, check func(*testing.T, faultfs.FS))`
  - `var errNoMutations = errors.New(...)`
  - test-only: `func Plan(r *Recorder, m Model) ([]string, error)` returning state names

- [ ] **Step 1: Write the failing test**

```go
package crash_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// A scenario that changed nothing has no crash point and no subtest. Run would
// return having asserted nothing, which reads exactly like a complete pass.
// This is the core's errNoOperations wearing a different coat.
func TestAScenarioThatChangedNothingIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Read(make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, err := crash.Plan(rec, crash.Model{}); err == nil {
		t.Fatal("a read-only scenario produced no diagnostic, so Run would report a pass")
	}
}

func TestARefusalFromTheRecorderReachesPlan(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere")
	rec := crash.Record(faultfs.OS(), dir)

	if f, err := rec.OpenFile(outside, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		_ = f.Close()
	}

	if _, err := crash.Plan(rec, crash.Model{}); err == nil {
		t.Fatal("a name outside root did not reach plan")
	}
}

// The whole point: a store that writes straight to its destination loses data,
// and the sweep says so.
func TestRunFindsAnInPlaceWrite(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(filepath.Join(dir, "data"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("v2")); err != nil {
		t.Fatal(err)
	}
	_ = f.Sync()
	_ = f.Close()

	names, err := crash.Plan(rec, crash.Model{Sector: 1})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(names) < 2 {
		t.Fatalf("got %d states, want at least 2 — an in-place write must produce a torn one", len(names))
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	build := func() []string {
		dir := t.TempDir()
		rec := crash.Record(faultfs.OS(), dir)
		f, _ := rec.OpenFile(filepath.Join(dir, "tmp"), os.O_CREATE|os.O_RDWR, 0o600)
		_, _ = f.Write([]byte("hello"))
		_ = f.Close()
		_ = rec.Rename(filepath.Join(dir, "tmp"), filepath.Join(dir, "data"))
		names, err := crash.Plan(rec, crash.Model{})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		return names
	}

	first := build()
	for i := 0; i < 5; i++ {
		got := build()
		if len(got) != len(first) {
			t.Fatalf("run %d gave %d states, first gave %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("run %d state %d is %q, first was %q — a map reached the order", i, j, got[j], first[j])
			}
		}
	}
	_ = errors.New
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestAScenarioThatChanged|TestARefusalFrom|TestRunFinds|TestPlanIsDeterministic' -v`
Expected: FAIL to build with `undefined: crash.Plan`.

- [ ] **Step 3: Write minimal implementation**

```go
package crash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

var errNoMutations = errors.New("crash: the scenario changed nothing, so there is no crash point and the sweep proved nothing")

// state is one candidate directory, with the name that identifies it.
type state struct {
	point  int
	lost   []unit
	name   string
	result tree
}

// plan builds every state the model allows, and returns a diagnostic rather
// than failing a test.
//
// The split exists for the same reason the core splits walk out of Sweep: a
// test cannot observe t.Fatal on its own *testing.T, and testing.TB carries an
// unexported method, so nothing outside the testing package can supply a
// stand-in. Without the split, the four most important error paths here are
// unreachable from any test.
func plan(r *Recorder, m Model) ([]state, error) {
	if err := r.failure(); err != nil {
		return nil, err
	}
	if err := r.checkReplay(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	entries := append([]entry(nil), r.entries...)
	snap := r.snap
	r.mu.Unlock()

	points := crashPoints(entries)
	if len(points) == 0 {
		return nil, errNoMutations
	}

	byIndex := index(entries)
	seen := map[string]bool{}
	var out []state

	for _, k := range points {
		durable, pending := split(entries, k, m)
		us := units(entries, pending, k, m)

		lostSets, err := subsets(us, m.Cover)
		if err != nil {
			return nil, fmt.Errorf("at crash point %d (%s): %w", k, pointName(byIndex, k), err)
		}

		for _, lost := range lostSets {
			keep := map[int]bool{}
			for _, n := range durable {
				keep[n] = true
			}
			partial := map[int][]unit{}
			for _, n := range pending {
				keep[n] = true
			}
			for _, u := range lost {
				if u.whole() {
					keep[u.entry] = false
					continue
				}
				partial[u.entry] = append(partial[u.entry], u)
			}

			keep = closure(entries, keep)
			built, err := replay(snap, entries, keep, partial)
			if err != nil {
				return nil, err
			}

			fp := fingerprint(built)
			if seen[fp] {
				continue
			}
			seen[fp] = true

			out = append(out, state{
				point:  k,
				lost:   lost,
				name:   stateName(byIndex, lost),
				result: built,
			})
		}
	}

	// A stable order, because Go randomises map iteration and -run must match
	// the same name on every run.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].point != out[j].point {
			return out[i].point < out[j].point
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// Run rebuilds every state the model allows and runs check on each, one
// subtest for each state.
//
// Run does not call t.Parallel. The states are independent, so the caller may
// call it inside check, and that is the caller's decision to make.
func Run(t *testing.T, rec *Recorder, m Model, check func(*testing.T, faultfs.FS)) {
	t.Helper()

	states, err := plan(rec, m)
	if err != nil {
		t.Fatal(err)
	}

	byPoint := map[int][]state{}
	var order []int
	for _, s := range states {
		if _, ok := byPoint[s.point]; !ok {
			order = append(order, s.point)
		}
		byPoint[s.point] = append(byPoint[s.point], s)
	}

	rec.mu.Lock()
	byIndex := index(rec.entries)
	root := rec.root
	rec.mu.Unlock()

	for _, k := range order {
		t.Run(pointName(byIndex, k), func(t *testing.T) {
			for _, s := range byPoint[k] {
				t.Run(s.name, func(t *testing.T) {
					dir := t.TempDir()
					if err := s.result.writeTo(dir); err != nil {
						t.Fatalf("cannot materialise the state: %v", err)
					}
					check(t, &remapFS{base: faultfs.OS(), from: root, to: dir})
				})
			}
		})
	}
}

// remapFS serves the rebuilt directory under the names the scenario used, so
// the check opens the store with the same path constants the run wrote with.
type remapFS struct {
	base     faultfs.FS
	from, to string
}

func (m *remapFS) path(name string) string {
	rel, err := filepath.Rel(m.from, name)
	if err != nil || strings.HasPrefix(rel, "..") {
		return name
	}
	return filepath.Join(m.to, rel)
}

func (m *remapFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	return m.base.OpenFile(m.path(name), flag, perm)
}
func (m *remapFS) Remove(name string) error { return m.base.Remove(m.path(name)) }
func (m *remapFS) Rename(o, n string) error { return m.base.Rename(m.path(o), m.path(n)) }
func (m *remapFS) Stat(name string) (os.FileInfo, error) { return m.base.Stat(m.path(name)) }
func (m *remapFS) MkdirAll(name string, perm os.FileMode) error {
	return m.base.MkdirAll(m.path(name), perm)
}
func (m *remapFS) ReadDir(name string) ([]os.DirEntry, error) { return m.base.ReadDir(m.path(name)) }
```

Add to `crash/export_test.go`:

```go
// Plan exposes the state names to tests, without a *testing.T.
func Plan(r *Recorder, m Model) ([]string, error) {
	states, err := plan(r, m)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(states))
	for i, s := range states {
		out[i] = pointNameFor(r, s.point) + "/" + s.name
	}
	return out, nil
}

func pointNameFor(r *Recorder, k int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return pointName(index(r.entries), k)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/run.go crash/run_test.go crash/export_test.go
git commit -m "feat(crash): one subtest for each state, and a plan that returns diagnostics a test can reach"
```

---

### Task 13: The four reference stores

**Files:**
- Create: `crash/stores_test.go`, `crash/crash_test.go`

**Interfaces:**
- Consumes: `Record`, `Run`, `Model`, `faultfs.FS`.
- Produces: test-only `safeStore`, `noFileSync`, `noDirSync`, `inPlace`, each with `save(fsys faultfs.FS, dir, value string) error` and a shared `open(fsys faultfs.FS, dir string) (string, error)`.

- [ ] **Step 1: Write the failing test**

`crash/stores_test.go`:

```go
package crash_test

import (
	"io"
	"os"
	"path/filepath"

	faultfs "github.com/dd0wney/fault/fs"
)

// The four reference stores. Two of them must fail, because a sweep that never
// fails anything is indistinguishable from a sweep that works.

// safeStore writes a temporary file, syncs it, renames it into place, and
// syncs the directory. It must survive every state under both models.
func safeStore(fsys faultfs.FS, dir, value string) error {
	tmp := filepath.Join(dir, "data.tmp")
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(value)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, filepath.Join(dir, "data")); err != nil {
		return err
	}
	return syncDir(fsys, dir)
}

// noFileSync never syncs the temporary file, so the rename can publish a name
// whose contents never reached the disk.
func noFileSync(fsys faultfs.FS, dir, value string) error {
	tmp := filepath.Join(dir, "data.tmp")
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(value)); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, filepath.Join(dir, "data")); err != nil {
		return err
	}
	return syncDir(fsys, dir)
}

// noDirSync syncs the file but never the directory, so the rename itself can
// be lost. It fails under the POSIX rule and passes under MetadataDurable,
// which makes it the only store that can catch that field being ignored.
func noDirSync(fsys faultfs.FS, dir, value string) error {
	tmp := filepath.Join(dir, "data.tmp")
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(value)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return fsys.Rename(tmp, filepath.Join(dir, "data"))
}

// inPlace writes straight to the destination. This is the store the package
// documentation names as the worked example, and no returned-error sweep
// catches it.
func inPlace(fsys faultfs.FS, dir, value string) error {
	f, err := fsys.OpenFile(filepath.Join(dir, "data"), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(value)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

// syncDir is how a Go program makes a rename durable: open the directory and
// sync the handle. It fails on Windows, which is why Model.MetadataDurable
// exists.
func syncDir(fsys faultfs.FS, dir string) error {
	d, err := fsys.OpenFile(dir, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// readStore is the check every reference test runs. A store is correct when
// the value it reads back is one of the two written values and never a mix.
func readStore(fsys faultfs.FS, dir string) (string, error) {
	f, err := fsys.OpenFile(filepath.Join(dir, "data"), os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	b, err := io.ReadAll(readerOf(f))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

type fileReader struct{ f faultfs.File }

func (r fileReader) Read(b []byte) (int, error) { return r.f.Read(b) }

func readerOf(f faultfs.File) io.Reader { return fileReader{f} }
```

`crash/crash_test.go`:

```go
package crash_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// verdict runs one reference store under one model and reports whether any
// state broke the invariant. It uses Plan rather than Run, because a test that
// asserts a store FAILS cannot let that failure reach the enclosing *testing.T.
func verdict(t *testing.T, save func(faultfs.FS, string, string) error, m crash.Model) bool {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// TWO saves, not one, and that is the whole discriminating power of this
	// table.
	//
	// With a single save, "v1" is a legal answer -- the crash simply happened
	// before the save completed -- so losing the rename is permitted and
	// noDirSync SURVIVES the POSIX model. That would leave the one row able to
	// catch MetadataDurable being read and ignored passing while proving
	// nothing, which is a false negative and looks exactly like success.
	//
	// With two saves, v2's publication COMPLETED before v3 began, so a state
	// that reads "v1" has reverted acknowledged data. That is a real defect and
	// every store that has it fails here.
	rec := crash.Record(faultfs.OS(), dir)
	if err := save(rec, dir, "v2"); err != nil {
		t.Fatalf("the first save failed during recording: %v", err)
	}
	if err := save(rec, dir, "v3"); err != nil {
		t.Fatalf("the second save failed during recording: %v", err)
	}

	states, err := crash.States(rec, m)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, s := range states {
		got, readErr := readStore(s.FS(t), dir)
		if readErr != nil {
			return false // the store did not reopen at all
		}
		// Never "v1": that is a reversion of data the store acknowledged.
		// Never anything else: that is a part-written record.
		if got != "v2" && got != "v3" {
			return false
		}
	}
	return true
}

func TestTheFourReferenceStores(t *testing.T) {
	cases := []struct {
		name   string
		save   func(faultfs.FS, string, string) error
		model  crash.Model
		survives bool
	}{
		{"safeStore/posix", safeStore, crash.Model{}, true},
		{"safeStore/metadataDurable", safeStore, crash.Model{MetadataDurable: true}, true},
		{"noFileSync/posix", noFileSync, crash.Model{}, false},
		{"noFileSync/metadataDurable", noFileSync, crash.Model{MetadataDurable: true}, false},
		{"noDirSync/posix", noDirSync, crash.Model{}, false},
		{"noDirSync/metadataDurable", noDirSync, crash.Model{MetadataDurable: true}, true},
		{"inPlace/posix", inPlace, crash.Model{Sector: 1}, false},
		{"inPlace/metadataDurable", inPlace, crash.Model{Sector: 1, MetadataDurable: true}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := verdict(t, c.save, c.model); got != c.survives {
				t.Errorf("survives = %v, want %v", got, c.survives)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run TestTheFourReferenceStores -v`
Expected: FAIL to build with `undefined: crash.States`.

- [ ] **Step 3: Write minimal implementation**

Add to `crash/export_test.go` the test-visible state handle the table needs:

```go
import (
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

// State is a test-visible candidate state.
type State struct {
	name string
	tree tree
	root string
}

// FS materialises the state and returns an fs.FS that serves it under the
// original names.
func (s State) FS(t *testing.T) faultfs.FS {
	t.Helper()
	dir := t.TempDir()
	if err := s.tree.writeTo(dir); err != nil {
		t.Fatalf("cannot materialise state %s: %v", s.name, err)
	}
	return &remapFS{base: faultfs.OS(), from: s.root, to: dir}
}

// States exposes plan's output to tests that must assert a store FAILS, which
// cannot be done through Run without failing the enclosing test.
func States(r *Recorder, m Model) ([]State, error) {
	got, err := plan(r, m)
	if err != nil {
		return nil, err
	}
	out := make([]State, len(got))
	for i, s := range got {
		out[i] = State{name: s.name, tree: s.result, root: r.root}
	}
	return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -run TestTheFourReferenceStores -v`
Expected: PASS, 8 subtests.

If `noDirSync/posix` reports `survives = true`, the durability model is not distinguishing a file sync from a directory sync. Fix Task 7, not this test.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/stores_test.go crash/crash_test.go crash/export_test.go
git commit -m "test(crash): four reference stores, two of which must fail"
```

---

### Task 14: Metamorphic properties, hand-worked counts, and Windows

**Files:**
- Create: `crash/properties_test.go`, `crash/windows_test.go`
- Test: both of the above

**Interfaces:**
- Consumes: `crash.States`, `crash.Plan`, the reference stores.
- Produces: no new production symbols.

- [ ] **Step 1: Write the failing test**

`crash/properties_test.go`:

```go
package crash_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

func record(t *testing.T, save func(faultfs.FS, string, string) error) (*crash.Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)
	if err := save(rec, dir, "v2"); err != nil {
		t.Fatalf("scenario: %v", err)
	}
	// ONE save here, deliberately, unlike the two in crash_test.go's verdict.
	// These tests compare state SETS against each other and never judge whether
	// a store is correct, so the second save would only enlarge the state space
	// without discriminating anything.
	return rec, dir
}

// Prefixes must be a subset of Exhaustive over a real record, not only over a
// hand-built unit list.
func TestPrefixStatesAreASubsetOfExhaustiveStates(t *testing.T) {
	rec, _ := record(t, safeStore)

	all, err := crash.Plan(rec, crash.Model{Cover: crash.Exhaustive})
	if err != nil {
		t.Fatalf("exhaustive: %v", err)
	}
	pre, err := crash.Plan(rec, crash.Model{Cover: crash.Prefixes})
	if err != nil {
		t.Fatalf("prefixes: %v", err)
	}

	seen := map[string]bool{}
	for _, n := range all {
		seen[n] = true
	}
	for _, n := range pre {
		if !seen[n] {
			t.Errorf("prefix state %q is not among the exhaustive states", n)
		}
	}
}

// A sector larger than every write cannot split anything, so it must give the
// same states as a whole-call model.
func TestAHugeSectorEqualsWholeCallLoss(t *testing.T) {
	rec, _ := record(t, safeStore)

	whole, err := crash.Plan(rec, crash.Model{Sector: 0})
	if err != nil {
		t.Fatal(err)
	}
	huge, err := crash.Plan(rec, crash.Model{Sector: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}

	if len(whole) != len(huge) {
		t.Fatalf("whole-call gave %d states, a 1 MiB sector gave %d", len(whole), len(huge))
	}
	for i := range whole {
		if whole[i] != huge[i] {
			t.Errorf("state %d differs: %q vs %q", i, whole[i], huge[i])
		}
	}
}

// A generator that quietly stops producing states is otherwise invisible,
// because fewer subtests still all pass. The number below is written down
// after running the test once and reading what it printed -- never from
// memory, and never before the run.
func TestTheStateCountForAKnownRecord(t *testing.T) {
	rec, _ := record(t, inPlace)

	got, err := crash.Plan(rec, crash.Model{})
	if err != nil {
		t.Fatal(err)
	}

	// MEASURED, not guessed. Run `go test -run TestTheStateCountForAKnownRecord
	// -v` once, read the count it reports, and write it here. If this number
	// changes, either the generator changed or a defect arrived; both need a
	// human to look.
	const want = 0 // replace with the measured value on the first run
	if want == 0 {
		t.Fatalf("the measured state count is missing: this record produces %d states — write that number into the const above", len(got))
	}
	if len(got) != want {
		t.Errorf("got %d states, want %d", len(got), want)
	}
}
```

`crash/windows_test.go`:

```go
//go:build !windows

package crash_test

import "testing"

// The POSIX metadata rule cannot hold on Windows, where a directory handle
// cannot be synced: syncDir returns an error there, so a store that obeys
// POSIX cannot obey it. Every test that calls syncDir therefore builds only
// off Windows. The MetadataDurable path runs everywhere, and it lives in
// crash_test.go with no build tag.
//
// CI decides whether this reasoning is right. A local gate says nothing about
// Windows.
func TestTheDirectorySyncPathIsBuiltHere(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// The presence of this file in the build is the assertion. It exists so
	// that the build tag has a stated reason next to it rather than sitting
	// alone at the top of a test file.
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crash/ -run 'TestPrefixStates|TestAHugeSector|TestTheStateCount' -v`
Expected: `TestTheStateCountForAKnownRecord` FAILS with the measured count in its message. The other two should pass.

- [ ] **Step 3: Write the measured number**

Read the count the failing test printed. Replace `const want = 0` with it. Move the reference stores that call `syncDir` (`safeStore`, `noFileSync`) behind the `!windows` build constraint by splitting `stores_test.go` into `stores_test.go` (`noDirSync`, `inPlace`, `readStore`) and `stores_dirsync_test.go` (`//go:build !windows`, holding `safeStore`, `noFileSync`, `syncDir`), and guard the table rows that use them the same way.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crash/ -v && GOOS=windows go vet ./crash/ && GOOS=windows go test -c -o /dev/null ./crash/`
Expected: PASS locally, and the Windows vet and compile both succeed. Neither proves Windows behaviour — only CI does.

- [ ] **Step 5: Commit**

```bash
gofmt -l crash/ && golangci-lint run ./crash/...
git add crash/properties_test.go crash/windows_test.go crash/stores_test.go crash/stores_dirsync_test.go
git commit -m "test(crash): the properties, a measured state count, and the Windows split"
```

---

### Task 15: Documentation, measured constants, and the gates

**Files:**
- Create: `crash/doc.go`
- Modify: `crash/record.go` (`maxBytes`), `crash/state.go` (`maxStates`), `crash/name.go` (`maxNameLen`), `README.md`, `scripts/mutation-baseline.tsv`, `docs/couplings.tsv`
- Test: existing suite plus the repository gates

**Interfaces:**
- Consumes: everything.
- Produces: no new exported symbols. The exported surface stays at seven.

- [ ] **Step 1: Measure the three provisional constants**

Write a throwaway test that prints, for each of the four reference stores under `Model{Sector: 4096}`: the record's byte total, the largest pending-unit count at any crash point, and the longest state name. Run it, read the numbers, then delete the test.

```bash
go test ./crash/ -run TestMeasureTheConstants -v
```

Set each constant to a value with clear headroom over the measured maximum, and write the measured number into the comment beside it. Do not write a number you did not read from that run.

- [ ] **Step 2: Write the package documentation**

`crash/doc.go` states, in the voice of `fs/fs.go`:
- what the package does, and that it is SQLite's third loop;
- the durability model of spec §5, including that `Close` flushes nothing;
- the record-once mechanism and why re-running does not produce power loss;
- the positive control, and that a failure of it means every finding is a fiction;
- the three limits of spec §10, in full: no crash during recovery, no filesystem whose reordering the model does not express, and nothing the check does not assert.

- [ ] **Step 3: Run every gate**

```bash
go test ./... -count=1
scripts/no-external-deps-selftest.sh && scripts/no-external-deps.sh
scripts/mutation-selftest.sh && scripts/mutation.sh
gofmt -l . && golangci-lint run ./...
GOOS=windows go vet ./... && GOOS=darwin go vet ./...
```

Expected: all pass. The mutation gate will report `crash` as a new package with no floor. Add its row to `scripts/mutation-baseline.tsv` using the score the gate reports, not a target.

- [ ] **Step 4: Add the coupling row and check the counts**

Add a `docs/couplings.tsv` row for the coupling that matters here: `crash.Run` depends on `fs.FS`'s method set, and a change to that interface breaks this package silently. Then run:

```bash
cd tools && go tool coupling && cd ..
```

Expected: the gate passes with the new row resolved. If it refuses the symbol as ambiguous, qualify it as `Receiver.Method`.

- [ ] **Step 5: Update the README and commit**

Add `fault/crash` to the README's package list with one sentence, and update the test-count line by **running** the command that produces it. Never write that number from memory.

```bash
go test ./... -count=1 -v 2>&1 | grep -c '^=== RUN'
```

```bash
gofmt -l . && golangci-lint run ./...
git add crash/ README.md scripts/mutation-baseline.tsv docs/couplings.tsv
git commit -m "docs(crash): the package documentation, measured constants, and the gates"
```

Then push and read the real CI result:

```bash
git push -u origin HEAD
gh run watch
gh run view --json conclusion
```

Do not report CI from the local gates. They were all green while Windows was red for eight minutes once already.

---

## Self-Review

**Spec coverage.**

| Spec section | Task |
|---|---|
| §5 durability model | 7 |
| §5.1 Windows | 7 (the `MetadataDurable` field), 14 (the build split) |
| §6 the record | 2 |
| §6.1 `needs` | 3, 9 |
| §6.2 initial snapshot | 4 |
| §6.3 byte budget | 2, 15 (the measured value) |
| §7 the generator's four steps | 7, 8, 9, 10 |
| §7.1 `Model` | 1 |
| §7.2 the state cap | 10, 15 (the measured value) |
| §8 public API | 12 |
| §8.1 call-site shape | 13 |
| §8.2 naming | 11 |
| §9.1 refusals | 2 (outside root, budget), 10 (cap), 12 (no mutations) |
| §9.2 positive control | 6 |
| §10 limitations | 15 (documented) |
| §11.1 four reference stores | 13 |
| §11.2 every refusal path | 2, 10, 12 |
| §11.3 the control's control | 6 |
| §11.4 metamorphic properties | 6, 10, 14 |
| §11.5 negative controls | 12, 14 |
| §11.6 determinism | 12 |
| §11.7 Windows | 14 |

**Type consistency.** `unit{entry, from, to}` is used with those field names in Tasks 8-12. `entry{n, k, path, to, off, size, data, dir, needs}` is used with those names in Tasks 2-12. `tree` and `node` in Tasks 4, 5, 9, 12. `Model`'s four fields keep the spec's names throughout.

**A gap that was found and then closed, recorded because the reasoning matters.** An earlier draft of Task 12 modelled a lost sector as zeroes. The graphdb session supplied the counter-example on 2026-08-30: `pkg/lsm/sstable_create.go:137` seeks to 0 and backpatches an SSTable header, so a lost sector there must keep the placeholder the same run wrote earlier. Zeroes would give `IndexOffset = 0`, which is structurally valid, so the reader would parse the body as an index and fail in a way it never fails in reality.

The fix removed a function rather than adding one. `replay` now applies only the ranges that landed, so a lost range simply keeps whatever was already there. No new information is needed, because the earlier write is already in the record.

**A genuine limit remains, and Task 15 must document it.** A lost sector of a write into a file that existed *before* `Record` was called keeps the snapshot's bytes, which is correct. There is no case left where the model invents content.
