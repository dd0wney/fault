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

// Run must fail the test when plan returns a diagnostic. That one line is the
// whole cost of the seam in run.go: plan is testable because it returns an
// error, and the line that turns the error into a failure is not.
//
// A test cannot contain a failing subtest and still pass, so this re-executes
// the test binary and asserts the child failed -- the pattern the core uses in
// TestSweepFailsTheTestWhenTheWalkProvesNothing, and the one os/exec's own
// tests use.
//
// MEASURED 2026-09-02: replacing `t.Fatal(err)` in Run with `_ = err` passed
// every test in this package. With it gone, a record the recorder refused --
// or one that changed nothing -- runs zero subtests, and Run returns having
// asserted nothing, which reads exactly like a complete pass.
func TestRunFailsTheTestWhenThePlanRefuses(t *testing.T) {
	if os.Getenv("FAULT_CRASH_CHILD") == "1" {
		// The child. A scenario that changed nothing has no crash point, so
		// plan refuses it, and Run must turn that refusal into a failure.
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
		crash.Run(t, rec, crash.Model{}, func(t *testing.T, _ faultfs.FS) {
			t.Error("the check ran, so Run built a state from a record that has no crash point")
		})
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "FAULT_CRASH_CHILD=1")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("the child test passed: Run did not fail a plan that refused\n%s", out)
	}
	if !strings.Contains(string(out), "proved nothing") {
		t.Errorf("the child failed without the diagnostic:\n%s", out)
	}
	// Run is a helper, so the line the failure names is the caller's. Without
	// that, every failing sweep names the same line inside run.go.
	if !strings.Contains(string(out), "run_fatal_test.go:") {
		t.Errorf("the failure is not reported at the caller's line:\n%s", out)
	}
}
