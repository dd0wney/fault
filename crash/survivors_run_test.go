package crash_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// Each test in this file pins a survivor of the mutation gate's run at
// ee92b62 through the public surface, where survivors_test.go pins the rest
// from inside the package. The comment on each test names the change it
// detects.

// Record on a root it cannot snapshot holds the refusal, and Plan reports it.
// Without the refusal the run starts from an empty snapshot of a directory
// that is not there, and the first message a caller sees is the control's.
func TestRecordRefusesARootItCannotSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	rec := crash.Record(faultfs.OS(), root)

	held := crash.Failure(rec)
	if held == nil {
		t.Fatal("Record held no refusal for a root that does not exist")
	}
	if !strings.Contains(held.Error(), "cannot snapshot the record root") {
		t.Fatalf("the held refusal is %v, want the snapshot one", held)
	}
	if _, err := crash.Plan(rec, crash.Model{}); !errors.Is(err, held) {
		t.Fatalf("plan returned %v, want the held refusal %v", err, held)
	}
}

// A directory alone, under the rule that metadata is durable, is one crash
// point with nothing pending, and so exactly one state. Three things must
// hold there. Prefixes must produce that one state without a panic, because
// its capacity hint is the unit count plus one. plan must not mistake one
// state for none. And Run must name a group of one without reading past it.
func TestOneStateIsAPlanAndNotARefusal(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)
	if err := rec.MkdirAll(filepath.Join(dir, "d"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := crash.Model{MetadataDurable: true, Cover: crash.Prefixes}

	names, err := crash.Plan(rec, m)
	if err != nil {
		t.Fatalf("plan refused a one-state record: %v", err)
	}
	if want := []string{"after=d:mkdir1/lost=none"}; !slices.Equal(names, want) {
		t.Fatalf("plan = %q, want %q", names, want)
	}

	states := 0
	crash.Run(t, rec, m, func(t *testing.T, fsys faultfs.FS) {
		states++
		if _, err := fsys.Stat(filepath.Join(dir, "d")); err != nil {
			t.Errorf("the one state lacks the directory: %v", err)
		}
	})
	if states != 1 {
		t.Errorf("Run ran %d states, want 1", states)
	}
}

// A check that removes the recorded root through os voids the run, as a check
// that writes to it does. Without the error report, a root that cannot be
// read after the check reads as "nothing changed" and the run passes. The
// child runs under the same re-exec pattern as the escape test.
func TestRunVoidsARunWhoseCheckRemovesTheRecordedRoot(t *testing.T) {
	if os.Getenv("FAULT_CRASH_REMOVE_CHILD") == "1" {
		rec, dir := inPlace(t)
		crash.Run(t, rec, crash.Model{}, func(t *testing.T, _ faultfs.FS) {
			// Through os, not through the filesystem the check was given.
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
		})
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "FAULT_CRASH_REMOVE_CHILD=1")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("the child test passed: Run did not void a run whose check removed the recorded root\n%s", out)
	}
	if !strings.Contains(string(out), "cannot read the recorded root after the check") {
		t.Errorf("the child failed without the diagnostic:\n%s", out)
	}
}

// The recorder answers after Run. Without the unlock in Run, the next call on
// the recorder blocks forever, and a caller that reads Observed after a sweep
// hangs. The read runs in a goroutine so a hang is a failure and not a
// timeout of the whole package.
func TestTheRecorderAnswersAfterRun(t *testing.T) {
	rec, _ := inPlace(t)
	crash.Run(t, rec, crash.Model{}, func(*testing.T, faultfs.FS) {})

	done := make(chan []string, 1)
	go func() { done <- rec.Observed() }()
	select {
	case got := <-done:
		if !slices.Contains(got, "data") {
			t.Errorf("Observed() = %q after Run, want it to name the path the run served", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Observed() blocked after Run, so Run left the recorder locked")
	}
}

// An open the base refused records nothing and returns the refusal. Without
// the return, the recorder hands back a handle over nil and records an open
// the filesystem never made.
func TestAFailedOpenRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "absent"), os.O_RDONLY, 0)
	if err == nil {
		t.Fatalf("OpenFile of a missing name returned a handle %v and no error", f)
	}
	if got := crash.Entries(rec); len(got) != 0 {
		t.Errorf("a failed open recorded %+v, want nothing", got)
	}
}

// A remove or a rename of a name outside the root is served, refused, and NOT
// recorded. Without the early return, an entry with an empty path joins the
// record, and Observed, which is the public view of it, would show that path.
func TestARefusedNameIsServedAndNotRecorded(t *testing.T) {
	calls := map[string]func(rec *crash.Recorder, other string) error{
		"remove": func(rec *crash.Recorder, other string) error {
			p := filepath.Join(other, "x")
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				return err
			}
			return rec.Remove(p)
		},
		"rename": func(rec *crash.Recorder, other string) error {
			p := filepath.Join(other, "x")
			if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
				return err
			}
			return rec.Rename(p, filepath.Join(other, "y"))
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			dir, other := t.TempDir(), t.TempDir()
			rec := crash.Record(faultfs.OS(), dir)
			seedThroughSeam(t, rec, dir, "through.dat")
			before := len(crash.Entries(rec))

			if err := call(rec, other); err != nil {
				t.Fatalf("%s outside the root was not served: %v", name, err)
			}
			if crash.Failure(rec) == nil {
				t.Fatal("the recorder held no refusal for a name outside the root")
			}
			if got := crash.Entries(rec); len(got) != before {
				t.Errorf("%s outside the root recorded %+v, want nothing", name, got[before:])
			}
			if got := rec.Observed(); slices.Contains(got, "") {
				t.Errorf("Observed() = %q, want no empty path", got)
			}
		})
	}
}

// A truncating open of a NEW path records the create alone. The path is
// already empty, and a truncate entry there would give the file two origins.
func TestATruncatingOpenOfANewPathRecordsOnlyTheCreate(t *testing.T) {
	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "new"), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, e := range crash.Entries(rec) {
		kinds = append(kinds, e.Kind)
	}
	if want := []string{"create", "open"}; !slices.Equal(kinds, want) {
		t.Errorf("a truncating open of a new path recorded %q, want %q", kinds, want)
	}
}

// A truncating open of an EXISTING path empties it before the first write,
// and the state that lost nothing at that point holds the empty file, not a
// prefix of the old bytes. The whole-record control cannot see this, because
// the write that follows covers the difference.
func TestATruncatingOpenEmptiesTheFileInTheStateAfterIt(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "data")
	if err := os.WriteFile(name, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(name, os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("n")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	states, err := crash.States(rec, crash.Model{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "after=data:truncate1/lost=none"
	found := false
	for _, s := range states {
		if s.Name != want {
			continue
		}
		found = true
		got, err := readStore(s.FS(t), dir)
		if err != nil {
			t.Fatalf("read the state %s: %v", s.Name, err)
		}
		if got != "" {
			t.Errorf("the state after the truncate holds %q, want an empty file", got)
		}
	}
	if !found {
		var names []string
		for _, s := range states {
			names = append(names, s.Name)
		}
		t.Fatalf("no state named %s among %q", want, names)
	}
}
