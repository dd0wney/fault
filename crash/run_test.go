package crash_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// The sentinel, not merely "an error". A damaged temporary directory would
	// also make plan return one, and a test that asked only for non-nil would
	// then pass while proving nothing about the refusal it names.
	if _, err := crash.Plan(rec, crash.Model{}); !errors.Is(err, crash.ErrNoMutations) {
		t.Fatalf("plan returned %v, want the no-mutation refusal — otherwise Run reports a pass", err)
	}
}

func TestARefusalFromTheRecorderReachesPlan(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "elsewhere")
	rec := crash.Record(faultfs.OS(), dir)

	if f, err := rec.OpenFile(outside, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
		_ = f.Close()
	}

	held := crash.Failure(rec)
	if held == nil {
		t.Fatal("the recorder held no refusal for a name outside root, so this test cannot prove plan surfaces one")
	}

	// The held refusal must come back, and it must come back BEFORE the replay
	// control. This record fails that control too, because it holds a create
	// under the empty path, so a test that asked only for non-nil could not
	// tell the two diagnostics apart.
	if _, err := crash.Plan(rec, crash.Model{}); !errors.Is(err, held) {
		t.Fatalf("plan returned %v, want the held refusal %v", err, held)
	}
}

// discardWriteFS hands back a File whose Write reports the whole buffer as
// written but moves no bytes.
//
// The byte budget counts what the RECORD holds, so this reaches the budget
// without putting 64 MiB on a disk. The record it builds is untruthful, and
// the replay control would say so -- which is the point: plan must return the
// held refusal before it reaches that control, and this test pins that order.
type discardWriteFS struct {
	faultfs.FS
}

func (d discardWriteFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	f, err := d.FS.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return discardWriteFile{f}, nil
}

type discardWriteFile struct {
	faultfs.File
}

func (d discardWriteFile) Write(b []byte) (int, error) { return len(b), nil }

func TestARecordOverItsByteBudgetIsRefused(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(discardWriteFS{faultfs.OS()}, dir)

	f, err := rec.OpenFile(filepath.Join(dir, "big"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// 72 MiB of recorded bytes against a 64 MiB budget. One buffer is reused,
	// so the test holds the recorded copies and nothing else.
	chunk := make([]byte, 8<<20)
	for i := 0; i < 9; i++ {
		if _, err := f.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	held := crash.Failure(rec)
	if held == nil {
		t.Fatal("the recorder held no refusal after 72 MiB against a 64 MiB budget")
	}
	if !strings.Contains(held.Error(), "byte budget") {
		t.Fatalf("the held refusal is %v, want the byte budget one", held)
	}
	if _, err := crash.Plan(rec, crash.Model{}); !errors.Is(err, held) {
		t.Fatalf("plan returned %v, want the byte budget refusal %v", err, held)
	}
}

func TestAnExhaustiveWalkPastTheCapIsRefused(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "data"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// One create plus thirteen one-byte sectors is fourteen pending units at
	// the write's crash point, so an exhaustive walk wants 16384 states.
	if _, err := f.Write([]byte("0123456789abc")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := crash.Plan(rec, crash.Model{Sector: 1}); !errors.Is(err, crash.ErrTooManyStates) {
		t.Fatalf("plan returned %v, want the state cap refusal", err)
	}

	// The diagnostic names Prefixes as the way forward, so Prefixes must
	// actually work on the same record. A cure that does not cure is worse
	// than no advice.
	if _, err := crash.Plan(rec, crash.Model{Sector: 1, Cover: crash.Prefixes}); err != nil {
		t.Fatalf("the refusal recommends Prefixes, but Prefixes failed: %v", err)
	}
}

// plan must run the replay control before it builds any state. Changing the
// directory behind the recorder's back makes the whole-record replay disagree
// with what is on disk, which is what a broken replay would look like.
func TestPlanRunsTheReplayControlBeforeBuildingStates(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	name := filepath.Join(dir, "data")
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Not through rec, so the record never learns of it.
	if err := os.WriteFile(name, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = crash.Plan(rec, crash.Model{})
	if err == nil {
		t.Fatal("plan built states from a record the replay control disagrees with")
	}
	if !strings.Contains(err.Error(), "did not reproduce the directory") {
		t.Fatalf("plan returned %v, want the replay control's diagnostic", err)
	}
}

// inPlace records a two-byte overwrite of a two-byte file, with no sync. It is
// the smallest scenario that tears, and its whole state set is short enough to
// write down.
func inPlace(t *testing.T) (*crash.Recorder, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("AA"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(filepath.Join(dir, "data"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("BB")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return rec, dir
}

// The whole point: a store that writes straight to its destination loses data,
// and the sweep says so. The four names below are worked by hand from the
// record, not read back from a run. A generator that quietly stops producing
// states is otherwise invisible, because fewer subtests still all pass.
func TestAnInPlaceWriteProducesEveryTornState(t *testing.T) {
	rec, _ := inPlace(t)

	got, err := crash.Plan(rec, crash.Model{Sector: 1})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	want := []string{
		"after=data:write1/lost=data:write1.s0",
		"after=data:write1/lost=data:write1.s0+data:write1.s1",
		"after=data:write1/lost=data:write1.s1",
		"after=data:write1/lost=none",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan gave\n  %q\nwant\n  %q", got, want)
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	build := func() []string {
		dir := t.TempDir()
		rec := crash.Record(faultfs.OS(), dir)

		f, err := rec.OpenFile(filepath.Join(dir, "tmp"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("hello")); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := rec.Rename(filepath.Join(dir, "tmp"), filepath.Join(dir, "data")); err != nil {
			t.Fatal(err)
		}

		names, err := crash.Plan(rec, crash.Model{})
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		return names
	}

	first := build()
	if len(first) < 2 {
		t.Fatalf("the fixture gives %d states, which is too few for an order to be observable", len(first))
	}
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
}

// Run must hand the check the REBUILT directory, under the names the scenario
// used. If it handed over the recorded root instead, every state would read
// "BB", the finished value, and the sweep would pass while examining nothing.
func TestRunGivesEachCheckItsOwnRebuiltState(t *testing.T) {
	rec, dir := inPlace(t)

	var got []string
	crash.Run(t, rec, crash.Model{Sector: 1}, func(t *testing.T, fsys faultfs.FS) {
		f, err := fsys.OpenFile(filepath.Join(dir, "data"), os.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer func() { _ = f.Close() }()

		buf := make([]byte, 2)
		if _, err := f.Read(buf); err != nil {
			t.Fatalf("read: %v", err)
		}
		got = append(got, string(buf))
	})

	// Run calls no t.Parallel, so the subtests ran one after another and all of
	// them finished before Run returned. That is what makes both the append
	// above and the read below safe without a lock.
	sort.Strings(got)
	want := []string{"AA", "AB", "BA", "BB"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the checks saw %q, want %q — one for each state, none of them the recorded root", got, want)
	}
}

// A crash at k cannot be survived by work that had not happened yet. Every
// state of the first write must therefore hold the file as the SECOND write
// found it, never as the second write left it. A crash point that leaked its
// successors would still produce a full set of subtests, and they would all
// pass, so nothing else in this file would notice.
func TestAStateNeverHoldsWorkFromAfterItsCrashPoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data"), []byte("AA"), 0o600); err != nil {
		t.Fatal(err)
	}

	rec := crash.Record(faultfs.OS(), dir)
	f, err := rec.OpenFile(filepath.Join(dir, "data"), os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"B", "C"} {
		if _, err := f.Write([]byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	states := 0
	crash.Run(t, rec, crash.Model{}, func(t *testing.T, fsys faultfs.FS) {
		states++
		content := readAll(t, fsys, filepath.Join(dir, "data"))
		if !strings.Contains(t.Name(), "after=data:write1/") {
			return
		}
		if strings.Contains(content, "C") {
			t.Errorf("a state of the first write holds %q, which only the second write could have put there", content)
		}
	})

	// Worked by hand: the first write gives "BA" and "AA", and the second
	// gives "BC", "AC", "BA" and "AA".
	//
	// The last two repeat the first write's pair byte for byte, and they are
	// still their own states, because plan deduplicates within one crash point
	// and not across the walk. "BA" after the first write and "BA" after the
	// second are two different situations: the second write had returned in
	// one of them and not in the other, and a check that knows what the store
	// acknowledged judges them differently.
	if states != 6 {
		t.Errorf("Run ran %d states, want 6", states)
	}
}

// readAll reads a whole small file through the filesystem under test.
func readAll(t *testing.T, fsys faultfs.FS, name string) string {
	t.Helper()
	f, err := fsys.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()

	var out []byte
	buf := make([]byte, 64)
	for {
		n, err := f.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			return string(out)
		}
	}
}

// A name outside the recorded root must be refused, not passed through to the
// real filesystem. Passing it through would let a check read the directory the
// run actually wrote, find everything intact, and report a pass for a crash
// state it never opened.
func TestTheRebuiltFilesystemRefusesANameOutsideTheRoot(t *testing.T) {
	rec, dir := inPlace(t)

	elsewhere := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(elsewhere, []byte("XX"), 0o600); err != nil {
		t.Fatal(err)
	}

	crash.Run(t, rec, crash.Model{}, func(t *testing.T, fsys faultfs.FS) {
		if f, err := fsys.OpenFile(elsewhere, os.O_RDONLY, 0); err == nil {
			_ = f.Close()
			t.Errorf("the rebuilt filesystem opened %q, which lies outside the recorded root", elsewhere)
		}
		if err := fsys.Rename(filepath.Join(dir, "data"), elsewhere); err == nil {
			t.Errorf("the rebuilt filesystem renamed into %q, which lies outside the recorded root", elsewhere)
		}
	})

	// The check must not have reached the file, in either direction.
	if b, err := os.ReadFile(elsewhere); err != nil || string(b) != "XX" {
		t.Errorf("the file outside the root now reads %q (%v), want %q untouched", b, err, "XX")
	}
}

// A state name is a go test -run address, so two models that produce identical
// states must address them identically. A sector larger than every write can
// split nothing, so it must name what a whole-call model names.
func TestAHugeSectorNamesTheSameStatesAsAWholeCallModel(t *testing.T) {
	rec, _ := inPlace(t)

	whole, err := crash.Plan(rec, crash.Model{Sector: 0})
	if err != nil {
		t.Fatalf("whole-call: %v", err)
	}
	huge, err := crash.Plan(rec, crash.Model{Sector: 1 << 20})
	if err != nil {
		t.Fatalf("huge sector: %v", err)
	}
	if !reflect.DeepEqual(whole, huge) {
		t.Errorf("Sector 0 gave\n  %q\na 1 MiB sector gave\n  %q", whole, huge)
	}
}

// The replay control must run BEFORE any state is built, and nothing pinned
// that. The code order was right, so moving the control after the state loop
// left the whole suite green: no fictional state escapes either way. What
// escapes is the work — up to 4096 replays for each crash point, spent before a
// refusal that was already certain.
//
// This record fails BOTH checks, with different diagnostics. It is damaged, so
// the control fails, and it carries fourteen pending units at the second crash
// point, so an exhaustive walk passes the state cap. Whichever check runs first
// names itself in the error.
func TestTheReplayControlRunsBeforeTheStateWalkNotAfterIt(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	name := filepath.Join(dir, "data")
	f, err := rec.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	// One create plus thirteen one-byte sectors is fourteen pending units, so
	// an exhaustive walk wants 16384 states and refuses.
	if _, err := f.Write([]byte("0123456789abc")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Not through rec, so the record never learns of it and the whole-record
	// replay stops matching the disk.
	if err := os.WriteFile(name, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = crash.Plan(rec, crash.Model{Sector: 1})
	if err == nil {
		t.Fatal("plan accepted a record that fails both the control and the state cap")
	}
	if errors.Is(err, crash.ErrTooManyStates) {
		t.Fatalf("plan returned the state cap refusal (%v), so it walked the states before it ran the control", err)
	}
	if !strings.Contains(err.Error(), "did not reproduce the directory") {
		t.Fatalf("plan returned %v, want the replay control's diagnostic", err)
	}
}

// Two crash points can rebuild the same bytes, and both states must survive.
//
// The crash point says how much the store had acknowledged when the power
// went, so identical trees at two points are two situations, not one. Dropping
// the later one collapses towards the EARLIER point, which is the one where
// less had been acknowledged and more values are still legal, so the walk
// under-reports. A missed finding is invisible, and the reference table caught
// exactly this: a store that reverted acknowledged data at one crash point had
// that state swallowed by an innocent duplicate at an earlier one.
//
// The fixture is the smallest one that shows it. The create of "a" does not
// depend on the mkdir of "d", so the crash point at the create can lose either
// name independently, and two of its four subsets rebuild trees the mkdir's
// own crash point already produced.
func TestTwoCrashPointsKeepIdenticalTreesApart(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	if err := rec.MkdirAll(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := rec.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := crash.Plan(rec, crash.Model{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// The last two names are the ones a walk that deduplicated globally lost:
	// losing the create leaves the mkdir's own "lost=none" tree, and losing
	// both leaves its "lost=d:mkdir1" tree.
	want := []string{
		"after=d:mkdir1/lost=d:mkdir1",
		"after=d:mkdir1/lost=none",
		"after=a:create1/lost=a:create1",
		"after=a:create1/lost=a:create1+d:mkdir1",
		"after=a:create1/lost=d:mkdir1",
		"after=a:create1/lost=none",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan gave\n  %q\nwant\n  %q", got, want)
	}
}

// The write before its sync must be losable at the write's own crash point.
//
// This is the whole point of the third loop, and no test pinned it at the
// plan level. A split that reads entries past the crash point sees the sync
// at 3, marks the file synced, and calls the write at 2 durable -- so the
// state "after=data:write1/lost=data:write1" is never built, and a store
// that loses that write on a power cut is never asked to survive it.
// MEASURED 2026-09-02: with that guard deleted, every test in this package
// passed. The list is pinned in full so a state that appears is caught as
// well as one that disappears.
func TestTheWriteBeforeItsSyncCanBeLost(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "data"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := crash.Plan(rec, crash.Model{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// At the write's crash point the sync has not run, so the create and the
	// write are both pending. Losing the create takes the write with it, so
	// that state and "lost both" rebuild the same empty tree and dedup to one.
	want := []string{
		"after=data:create1/lost=data:create1",
		"after=data:create1/lost=none",
		"after=data:write1/lost=data:create1",
		"after=data:write1/lost=data:write1",
		"after=data:write1/lost=none",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("plan gave\n  %q\nwant\n  %q", got, want)
	}
}
