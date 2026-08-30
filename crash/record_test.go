package crash_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
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

// The offset a handle tracks is Recorder state, not file state, so two
// goroutines that share one handle touch it concurrently. Review finding 1
// caught a data race here: -race reported it at record.go:260 inside seconds
// once a Write moved the offset outside the lock. This is the regression
// guard for that fix.
func TestConcurrentWritesOnOneHandleDoNotRace(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	name := filepath.Join(dir, "a")
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const goroutines = 4
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if _, err := f.Write([]byte("x")); err != nil {
				t.Errorf("write: %v", err)
			}
		}()
	}
	wg.Wait()

	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// shortWriteFS wraps a real FS but hands back a File whose Write moves at
// most half the offered buffer, without reporting an error. It exists to
// drive TestAShortWriteRecordsOnlyWhatLanded, which review finding 3 asked
// for: nothing checked that a torn write is recorded as torn.
type shortWriteFS struct {
	faultfs.FS
}

func (s shortWriteFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	f, err := s.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return shortWriteFile{f}, nil
}

type shortWriteFile struct {
	faultfs.File
}

func (s shortWriteFile) Write(b []byte) (int, error) {
	half := len(b) / 2
	if half == 0 && len(b) > 0 {
		half = 1
	}
	return s.File.Write(b[:half])
}

// data must hold only the bytes the base actually wrote, never the whole
// buffer a caller offered. Review finding 3 found this property untested.
func TestAShortWriteRecordsOnlyWhatLanded(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(shortWriteFS{faultfs.OS()}, dir)

	name := filepath.Join(dir, "a")
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	offered := []byte("hello world")
	n, err := f.Write(offered)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n == len(offered) {
		t.Fatalf("the stub did not shorten the write, so this test proves nothing")
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	write := findEntry(t, crash.Entries(rec), "write")
	if len(write.Data) != n {
		t.Errorf("recorded %d bytes, want %d (only what landed)", len(write.Data), n)
	}
	if string(write.Data) != string(offered[:n]) {
		t.Errorf("recorded data = %q, want %q", write.Data, offered[:n])
	}
}

// A read advances the offset, so a write that follows it must be recorded at
// the position after the read, not at zero. Review finding 3 found this
// property untested.
func TestAReadAdvancesTheOffsetForTheNextWrite(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	n, err := f.Read(make([]byte, 4))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := f.Write([]byte("X")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	write := findEntry(t, crash.Entries(rec), "write")
	if write.Off != int64(n) {
		t.Errorf("write recorded at offset %d, want %d (the bytes read)", write.Off, n)
	}
}

// delayedOpenFS wraps a real FS and sleeps before delegating OpenFile. A real
// disk call is fast enough that two goroutines opening the same new path
// might overlap the window between checking whether it exists and recording
// what happened, but only sometimes -- and a test that fails only sometimes
// is not a regression guard, it is noise. The sleep widens that window past
// the time it takes every goroutine in the test to reach it, so the overlap
// is reliable rather than hoped for.
type delayedOpenFS struct {
	faultfs.FS
	delay time.Duration
}

func (d delayedOpenFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	time.Sleep(d.delay)
	return d.FS.OpenFile(name, flag, perm)
}

// Two callers opening the same new path must not both record a create: one
// path gets one directory entry, and a second create would hand a later
// dependency an origin that does not correspond to what a crash could leave.
// Review finding 2 found the window this closes: existedBefore was read
// outside r.mu and acted on inside it, so two goroutines could both see "did
// not exist". OpenFile now holds r.mu across the whole call, the same shape
// Read and Write already use, which removes the window rather than narrowing
// it.
func TestConcurrentOpensOnOneNewPathRecordExactlyOneCreate(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(delayedOpenFS{FS: faultfs.OS(), delay: 5 * time.Millisecond}, dir)
	name := filepath.Join(dir, "a")

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Errorf("open: %v", err)
				return
			}
			_ = f.Close()
		}()
	}
	close(start) // release every goroutine at once, to force maximum overlap
	wg.Wait()

	creates := 0
	for _, e := range crash.Entries(rec) {
		if e.Kind == "create" {
			creates++
		}
	}
	if creates != 1 {
		t.Errorf("recorded %d create entries for one new path, want exactly 1", creates)
	}
}

// findEntry returns the first entry of kind, or fails the test. Two tests
// share this rather than repeating the same loop.
func findEntry(t *testing.T, entries []crash.Entry, kind string) crash.Entry {
	t.Helper()
	for _, e := range entries {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no %s entry recorded", kind)
	return crash.Entry{}
}

// O_TRUNC on a file that already exists resets its size, and the record has to
// say so.
//
// Without a truncate entry the replay keeps the tail of the older, longer
// value. The replay control then refuses the whole record, so "open, truncate,
// rewrite" -- a publish idiom as common as the temporary file and the rename
// -- cannot be swept at all, and the only way past it is to give every fixture
// values of one length, which hides the gap from everyone else.
func TestOTruncOnAnExistingFileIsRecordedAsATruncate(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "data")
	if err := os.WriteFile(name, []byte("the-older-longer-value"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(name, os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// plan runs the replay control before it builds anything, so an unmodelled
	// O_TRUNC arrives here as the control's diagnostic rather than as a state.
	states, err := crash.States(rec, crash.Model{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	findEntry(t, crash.Entries(rec), "truncate")

	// The state that lost nothing at the last crash point is the whole replay.
	const want = "after=data:write1/lost=none"
	var got []string
	for _, s := range states {
		got = append(got, s.Name)
		if s.Name != want {
			continue
		}
		if v := readAll(t, s.FS(t), name); v != "new" {
			t.Fatalf("the replayed tree holds %q, want %q — the tail of the older value outlived the truncate", v, "new")
		}
		return
	}
	t.Fatalf("no state is named %q; plan gave %q", want, got)
}

// The reviewer's measured case, and the worst failure this package can have:
// it reported a finding against correct code.
//
// `defer fsys.Remove(tmp)` after a successful rename is the ordinary idiom. The
// remove fails with ENOENT, because the rename already moved the name. The
// recorder recorded it anyway, so the log held a remove that never happened,
// and the walk built a state named after=a:remove1/lost=a:rename1 in which
// NEITHER name holds the value. No power cut can produce that state, so every
// finding against it is noise, and noise trains a reader to ignore the tool.
func TestARemoveThatTheBaseRefusedIsNotRecorded(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")

	f, err := rec.OpenFile(a, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rec.Rename(a, b); err != nil {
		t.Fatal(err)
	}

	// The idiom. The rename already moved the name, so this fails.
	if err := rec.Remove(a); err == nil {
		t.Fatal("the remove succeeded, so this test says nothing about a failed one")
	}

	for _, e := range crash.Entries(rec) {
		if e.Kind == "remove" {
			t.Errorf("a remove the base refused is in the record: %+v", e)
		}
	}

	states, err := crash.States(rec, crash.Model{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(states) == 0 {
		t.Fatal("no states at all, so the name check below proves nothing")
	}
	for _, s := range states {
		// after=a:remove1/lost=a:rename1 is the exact state the reviewer
		// measured: the remove is the crash point, the rename is lost, and
		// neither a nor b holds the value.
		if strings.Contains(s.Name, ":remove") {
			t.Errorf("state %q names a remove the run never performed", s.Name)
		}
	}
}

// failSyncFS hands back a handle whose Sync always fails. A sync that failed
// made nothing durable, so recording it lets the model believe a pending write
// reached the disk and drops the state where it did not. The reviewer reasoned
// this and could not measure it, because the base is a real filesystem.
type failSyncFS struct {
	faultfs.FS
}

func (s failSyncFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	f, err := s.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return failSyncFile{f}, nil
}

type failSyncFile struct {
	faultfs.File
}

func (s failSyncFile) Sync() error { return errors.New("the disk refused the sync") }

// A Sync whose base call failed must not enter the record, or the model makes
// every pending entry on that handle durable and stops generating the states a
// crash really can leave.
func TestASyncThatTheBaseRefusedIsNotRecorded(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(failSyncFS{faultfs.OS()}, dir)

	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err == nil {
		t.Fatal("the sync succeeded, so this test says nothing about a failed one")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	for _, e := range crash.Entries(rec) {
		if e.Kind == "sync" {
			t.Errorf("a sync the base refused is in the record: %+v", e)
		}
	}

	// The consequence, not only the entry: the write is still pending, so the
	// state that loses it must still be built.
	names, err := crash.Plan(rec, crash.Model{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	const want = "after=a:write1/lost=a:write1"
	for _, n := range names {
		if n == want {
			return
		}
	}
	t.Errorf("no state is named %q; plan gave %q — a failed sync made the write durable", want, names)
}

// MkdirAll creates every missing level, and the record has to say so. Recording
// only the deepest path left the replay with no entry for the parents, so the
// positive control refused the whole record with "missing: a" and the package
// could describe no scenario that builds a directory tree.
func TestMkdirAllRecordsOneEntryPerLevelItCreates(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	if err := rec.MkdirAll(filepath.Join(dir, "a", "b"), 0o700); err != nil {
		t.Fatal(err)
	}

	var mkdirs []crash.Entry
	for _, e := range crash.Entries(rec) {
		if e.Kind == "mkdir" {
			mkdirs = append(mkdirs, e)
		}
	}
	if len(mkdirs) != 2 {
		t.Fatalf("recorded %d mkdir entries for a/b, want 2 (one per level): %+v", len(mkdirs), mkdirs)
	}
	if mkdirs[0].Path != "a" || mkdirs[1].Path != "a/b" {
		t.Errorf("recorded %q then %q, want \"a\" then \"a/b\" — outermost first", mkdirs[0].Path, mkdirs[1].Path)
	}

	needs := crash.Needs(rec)
	if len(needs[0]) != 0 {
		t.Errorf("the mkdir of a needs %v, want none — its parent is the record root", needs[0])
	}
	if len(needs[1]) != 1 || needs[1][0] != mkdirs[0].N {
		t.Errorf("the mkdir of a/b needs %v, want [%d] — the mkdir of a", needs[1], mkdirs[0].N)
	}

	// A level that already exists is not created again, so it must not be
	// recorded again: two origins for one name is a record no crash matches.
	if err := rec.MkdirAll(filepath.Join(dir, "a", "c"), 0o700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range crash.Entries(rec) {
		if e.Kind == "mkdir" {
			paths = append(paths, e.Path)
		}
	}
	want := []string{"a", "a/b", "a/c"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("mkdir paths = %q, want %q", paths, want)
	}

	// This is what the reviewer measured failing, as "missing: a".
	if _, err := crash.States(rec, crash.Model{}); err != nil {
		t.Fatalf("the control refused a record that builds a directory tree: %v", err)
	}
}

// A rename the base refused changes nothing, so it must not enter the record.
// Recording it moves the origin of a name that never moved, and every later
// dependency hangs off an operation that did not happen.
func TestARenameThatTheBaseRefusedIsNotRecorded(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	if err := rec.Rename(filepath.Join(dir, "missing"), filepath.Join(dir, "data")); err == nil {
		t.Fatal("the rename succeeded, so this test says nothing about a failed one")
	}
	for _, e := range crash.Entries(rec) {
		if e.Kind == "rename" {
			t.Errorf("a rename the base refused is in the record: %+v", e)
		}
	}
}

// A MkdirAll the base refused creates nothing, so it must not enter the record.
func TestAMkdirAllThatTheBaseRefusedIsNotRecorded(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)

	if err := rec.MkdirAll(filepath.Join(dir, "a", "b"), 0o700); err == nil {
		t.Fatal("the mkdir succeeded, so this test says nothing about a failed one")
	}
	for _, e := range crash.Entries(rec) {
		if e.Kind == "mkdir" {
			t.Errorf("a mkdir the base refused is in the record: %+v", e)
		}
	}
}

// A Truncate the base refused changes no byte, so it must not enter the record.
// A handle opened read-only cannot be truncated.
func TestATruncateThatTheBaseRefusedIsNotRecorded(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "a")
	if err := os.WriteFile(name, []byte("0123"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(name, os.O_RDONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1); err == nil {
		t.Fatal("the truncate succeeded on a read-only handle, so this test says nothing")
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, e := range crash.Entries(rec) {
		if e.Kind == "truncate" {
			t.Errorf("a truncate the base refused is in the record: %+v", e)
		}
	}
}
