package crash_test

import (
	"os"
	"path/filepath"
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
