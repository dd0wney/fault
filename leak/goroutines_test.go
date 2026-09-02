package leak

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/dd0wney/fault/goroutine"
)

// TestGoroutinesNamesEachLeakedGoroutine is a re-exec test for the same
// reason as TestCheckFailsTheTestOnAHeldHandle: testing.TB cannot be faked.
func TestGoroutinesNamesEachLeakedGoroutine(t *testing.T) {
	if os.Getenv("FAULT_LEAK_CHILD") == "goroutine-leak" {
		snap := goroutine.Take()

		block := make(chan struct{})
		go func() { <-block }()

		Goroutines(t, snap, 50*time.Millisecond)
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "FAULT_LEAK_CHILD=goroutine-leak")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Errorf("the child test passed: Goroutines did not fail on a running goroutine\n%s", out)
	}
	// "chan receive" is the blocked goroutine's own state, so this names the
	// leaked goroutine specifically rather than merely detecting a failure.
	if !strings.Contains(string(out), "chan receive") {
		t.Errorf("the failure does not name the goroutine's state:\n%s", out)
	}
	if !strings.Contains(string(out), "still running") {
		t.Errorf("the failure does not read as a leak report:\n%s", out)
	}
}
