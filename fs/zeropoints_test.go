package fs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// A zero Points arms nothing, so this adapter must fail nothing.
//
// The core already proves that a zero Points never trips. This proves the
// other half, which is the adapter's own: that it fails an operation ONLY
// because Trip said so, and never for a reason of its own.
//
// THE GAP THIS CLOSES WAS FOUND IN ANOTHER ADAPTER, NOT BY READING THIS ONE.
// fault/sql's rows.Next held it until 2026-08-31: turning
//
//	if r.armed && r.n == r.at   into   if true && r.n == r.at
//
// changed no test result, and that version fails the first row of EVERY
// result set, armed or not. Every test in that suite armed something, so
// none could see it.
//
// THE MUTATION GATE CANNOT COVER THIS HERE. The mutant that expresses an
// invented failure rewrites one operand of a compound condition, so it only
// exists where the condition has two. This package writes `if f.p.Trip()`,
// which has one, so no mutant expresses it and this package still sits at a
// floor of 1.00. A clean mutation score is not evidence about this property,
// which is why the assertion is written by hand.
//
// An invented failure is worse than a missed one. A missed injection
// under-reports and its report stays true. An invented one reports a defect
// the code under test does not have, so every later finding needs a person to
// re-derive whether it is real.
func TestAZeroPointsFailsNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		make func(*fault.Points, faultfs.FS) faultfs.FS
	}{
		{"New", func(p *fault.Points, base faultfs.FS) faultfs.FS { return faultfs.New(p, base) }},
		{"NewShortWrite", func(p *fault.Points, base faultfs.FS) faultfs.FS { return faultfs.NewShortWrite(p, base) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			fsys := c.make(&fault.Points{}, faultfs.OS())

			if err := fsys.MkdirAll(filepath.Join(dir, "sub"), 0o750); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			name := filepath.Join(dir, "sub", "a")
			f, err := fsys.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatalf("OpenFile: %v", err)
			}

			// The whole buffer, and no short write. NewShortWrite moves half a
			// buffer and reports ENOSPC when it trips; with nothing armed it
			// must move all of it.
			payload := []byte("hello, world")
			n, err := f.Write(payload)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if n != len(payload) {
				t.Errorf("Write moved %d bytes of %d with nothing armed", n, len(payload))
			}

			if err := f.Sync(); err != nil {
				t.Errorf("Sync: %v", err)
			}
			if err := f.Truncate(4); err != nil {
				t.Errorf("Truncate: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}

			// A fresh handle, because the write left the first one at offset
			// 12 and a read there is EOF from the real filesystem rather than
			// anything this adapter did. A test that read through the old
			// handle would fail for a reason that is not the property under
			// test, which is its own kind of false report.
			r, err := fsys.OpenFile(name, os.O_RDONLY, 0)
			if err != nil {
				t.Fatalf("OpenFile for reading: %v", err)
			}
			if _, err := r.Read(make([]byte, 4)); err != nil {
				t.Errorf("Read: %v", err)
			}
			if err := r.Close(); err != nil {
				t.Errorf("Close after reading: %v", err)
			}

			if _, err := fsys.Stat(name); err != nil {
				t.Errorf("Stat: %v", err)
			}
			if _, err := fsys.ReadDir(filepath.Join(dir, "sub")); err != nil {
				t.Errorf("ReadDir: %v", err)
			}
			moved := filepath.Join(dir, "sub", "b")
			if err := fsys.Rename(name, moved); err != nil {
				t.Errorf("Rename: %v", err)
			}
			if err := fsys.Remove(moved); err != nil {
				t.Errorf("Remove: %v", err)
			}

			// Nothing was armed, so nothing was handed out and not returned.
			//
			// The two-value form because .golangci.yml sets
			// errcheck.check-type-assertions, and it is better here than the
			// one-value form for the same reason it was in fault/sql: it turns
			// "the constructor stopped returning the concrete type" from a
			// panic into a sentence.
			ff, ok := fsys.(*faultfs.Fault)
			if !ok {
				t.Fatalf("the constructor returned %T, not *faultfs.Fault, so Outstanding cannot be read", fsys)
			}
			if got := ff.Outstanding(); got != 0 {
				t.Errorf("Outstanding() = %d, want 0", got)
			}
		})
	}
}
