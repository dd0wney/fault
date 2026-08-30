//go:build !windows

// Both properties below measure a safeStore record, and safeStore calls
// syncDir to make its rename durable -- a call that fails on Windows, where a
// directory handle cannot be synced. This file therefore builds only off
// Windows, the same way stores_dirsync_test.go does for the stores
// themselves.

package crash_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
//
// The brief compared Plan's NAME strings. That comparison fails on correct
// code: at the crash point after the temporary file's write, losing the
// create alone collapses through closure to the same tree as losing the
// create and the write together, since the write needs the create. Exhaustive
// visits "lose the create" first (its mask is smaller) and keeps that name;
// Prefixes never visits "lose the create alone" -- only suffixes of log order
// are legal there -- so it names the very same tree "lose the create and the
// write". Two separate plan() calls dedup independently, so the representative
// name each keeps for one physical state can differ between covers. This was
// confirmed by running both covers over the same record and reading what each
// printed, not assumed.
//
// The property that does hold, and that a broken generator would still trip,
// is over the disk CONTENT a state materialises: every directory Prefixes can
// produce must be a directory Exhaustive can also produce, at the same crash
// point.
func TestPrefixStatesAreASubsetOfExhaustiveStates(t *testing.T) {
	rec, dir := record(t, safeStore)

	all, err := crash.States(rec, crash.Model{Cover: crash.Exhaustive})
	if err != nil {
		t.Fatalf("exhaustive: %v", err)
	}
	pre, err := crash.States(rec, crash.Model{Cover: crash.Prefixes})
	if err != nil {
		t.Fatalf("prefixes: %v", err)
	}

	seen := map[string]bool{}
	for _, s := range all {
		seen[snapshot(t, s, dir)] = true
	}
	for _, s := range pre {
		if !seen[snapshot(t, s, dir)] {
			t.Errorf("prefix state %q rebuilds a directory that no exhaustive state rebuilds", s.Name)
		}
	}
}

// snapshot renders the directory a state materialises as one comparable
// string: the crash point, plus every entry's name and bytes in a stable
// order. Two states with equal snapshots are the same disk content, whichever
// name plan's dedup happened to keep for either of them.
func snapshot(t *testing.T, s crash.State, dir string) string {
	t.Helper()
	fsys := s.FS(t)

	entries, err := fsys.ReadDir(dir)
	if err != nil {
		t.Fatalf("read state %s: %v", s.Name, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var b strings.Builder
	fmt.Fprintf(&b, "point=%d;", s.Point)
	for _, e := range entries {
		f, err := fsys.OpenFile(filepath.Join(dir, e.Name()), os.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("open %q in state %s: %v", e.Name(), s.Name, err)
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("read %q in state %s: %v", e.Name(), s.Name, err)
		}
		fmt.Fprintf(&b, "%s=%q;", e.Name(), data)
	}
	return b.String()
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
