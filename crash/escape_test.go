package crash_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// Run must void a run whose check changes the recorded root. The check is
// handed a filesystem rooted in a rebuilt copy, so a change to the root means
// the code under test reached the filesystem through the os package and acted
// on the tree the run copied from. doc.go records the finding that shape
// produced against a peer project, and the half of the hole that stays open.
//
// The check fires inside a subtest, so the parent cannot observe it directly.
// This re-executes the test binary, as TestRunFailsTheTestWhenThePlanRefuses
// does, and asserts the child failed with the diagnostic.
//
// The path assertion looks for the diffTree line and not the bare name,
// because the subtest names already carry "data" and a bare-name check would
// pass with the diagnostic missing entirely. A reviewer ran the child and
// saw that.
func TestRunVoidsARunWhoseCheckChangesTheRecordedRoot(t *testing.T) {
	if os.Getenv("FAULT_CRASH_ESCAPE_CHILD") == "1" {
		rec, dir := inPlace(t)
		crash.Run(t, rec, crash.Model{}, func(t *testing.T, _ faultfs.FS) {
			// Through os, not through the filesystem the check was given.
			if err := os.WriteFile(filepath.Join(dir, "data"), []byte("ZZ"), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "FAULT_CRASH_ESCAPE_CHILD=1")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("the child test passed: Run did not void a run whose check wrote to the recorded root\n%s", out)
	}
	if !strings.Contains(string(out), "recorded root") {
		t.Errorf("the child failed without the diagnostic:\n%s", out)
	}
	if !strings.Contains(string(out), "contents differ: data") {
		t.Errorf("the diagnostic does not name the changed path:\n%s", out)
	}
}

// The control: a check that writes through the filesystem it was given
// changes a rebuilt copy and not the recorded root, so Run must not void it.
// Without this, a check that fired on every write would pass the test above
// while making every real sweep fail.
func TestRunKeepsARunWhoseCheckWritesThroughTheGivenFilesystem(t *testing.T) {
	rec, dir := inPlace(t)
	states := 0
	crash.Run(t, rec, crash.Model{}, func(t *testing.T, fsys faultfs.FS) {
		states++
		f, err := fsys.OpenFile(filepath.Join(dir, "data"), os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("ZZ")); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	})
	// inPlace records one write with no sync, one whole unit under
	// Model{}, so the walk has exactly two states: the one that lost
	// nothing and the one that lost the write.
	if states != 2 {
		t.Fatalf("%d states ran, want 2: the one that lost nothing and the one that lost the write", states)
	}
}
