package app_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"

	"demo/app"
)

const payload = "hello, world"

// TestNormalRange is the normal range case: a zero Points arms nothing, so
// every operation succeeds and the scenario runs clean. This is the baseline
// the chain subtracts.
func TestNormalRange(t *testing.T) {
	dir := t.TempDir()
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())

	if err := app.Save(fsys, dir, []byte(payload)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload[:10] {
		t.Errorf("data holds %q, want the first 10 bytes %q", got, payload[:10])
	}
	if fsys.MaxOutstanding() == 0 {
		t.Fatal("nothing was ever opened, so the leak check below proves nothing")
	}
	if fsys.Outstanding() != 0 {
		t.Errorf("%d handles leaked", fsys.Outstanding())
	}
}

// TestSweep is the robustness case: each pass fails one operation, and the
// invariant is that a failed save leaves no partial file and no handle.
func TestSweep(t *testing.T) {
	// Asked of the scenario, not of each pass. The pass that arms the first
	// successful open holds nothing, and a per-pass check fired there on
	// correct code the first time this was written.
	opened := false
	for n, p := range fault.Sweep(t) {
		dir := t.TempDir()
		fsys := faultfs.New(p, faultfs.OS())

		err := app.Save(fsys, dir, []byte(payload))
		if fsys.MaxOutstanding() > 0 {
			opened = true
		}

		got, readErr := os.ReadFile(filepath.Join(dir, "data"))
		switch {
		case err == nil && string(got) != payload[:10]:
			t.Errorf("op %d: Save reported success and data holds %q", n, got)
		case err != nil && readErr == nil:
			t.Errorf("op %d: Save failed with %v and data exists holding %q", n, err, got)
		}
		if _, err := os.Stat(filepath.Join(dir, "data.tmp")); err == nil {
			t.Errorf("op %d: the temporary was left behind", n)
		}
		if fsys.Outstanding() != 0 {
			t.Errorf("op %d: %d handles leaked after %v", n, fsys.Outstanding(), err)
		}
	}
	if !opened {
		t.Error("no pass opened anything, so the leak checks proved nothing")
	}
}
