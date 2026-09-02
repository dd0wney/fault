package leak

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// testing.TB cannot be faked -- it has an unexported method -- so Check is
// proved by a re-executed child, the pattern of crash/run_fatal_test.go.
//
// MEASURED 2026-09-02: see the commit body for the two runs of this test
// with t.Errorf replaced by _ = inside Check. Neutered, this test must fail,
// because a Check that never reports anything reads exactly like one that
// passed.
func TestCheckFailsTheTestOnAHeldHandle(t *testing.T) {
	if os.Getenv("FAULT_LEAK_CHILD") == "held-handle" {
		dir := t.TempDir()
		name := filepath.Join(dir, "wal.log")
		if err := os.WriteFile(name, []byte("v1"), 0o600); err != nil {
			t.Fatal(err)
		}

		fsys := faultfs.New(&fault.Points{}, faultfs.OS())
		if _, err := fsys.OpenFile(name, os.O_RDONLY, 0); err != nil {
			t.Fatal(err)
		}
		// Deliberately not closed: this is the leak Check must report.
		Check(t, fsys)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "FAULT_LEAK_CHILD=held-handle")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("the child test passed: Check did not fail on a held handle\n%s", out)
	}
	if !strings.Contains(string(out), "still holds 1") {
		t.Errorf("the failure does not report the count:\n%s", out)
	}
	if !strings.Contains(string(out), "wal.log") {
		t.Errorf("the failure does not name the file:\n%s", out)
	}
}

// TestCheckGivenNothingFails is TestCheckFailsTheTestOnAHeldHandle's
// sibling: a second scenario in the same re-exec pattern, selected by a
// second value of the same environment variable.
func TestCheckGivenNothingFails(t *testing.T) {
	if os.Getenv("FAULT_LEAK_CHILD") == "no-counter" {
		Check(t)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "FAULT_LEAK_CHILD=no-counter")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("the child test passed: Check did not fail with no counter\n%s", out)
	}
	if !strings.Contains(string(out), "no counter") {
		t.Errorf("the failure does not say why:\n%s", out)
	}
}

// TestCheckPassesOnAScenarioThatOpenedAndClosed needs no re-exec for the
// passing half: a Check that does not fail need not be caught by anything.
// The "without the open" half is test 4 through the real adapter, and it is
// asserted through Report rather than Check, because a Check failure cannot
// be caught in-process.
func TestCheckPassesOnAScenarioThatOpenedAndClosed(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "wal.log")
	if err := os.WriteFile(name, []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	fsys := faultfs.New(&fault.Points{}, faultfs.OS())
	f, err := fsys.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	Check(t, fsys)

	untouched := faultfs.New(&fault.Points{}, faultfs.OS())
	got := Report(untouched)
	if len(got) != 1 || !strings.Contains(got[0], "never held anything") {
		t.Errorf("Report(untouched) = %v, want one sentence about never holding anything", got)
	}
}
