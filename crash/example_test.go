package crash_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// Observed names the paths the recorder actually served.
//
// It is the assertion a crash sweep needs BEFORE any durability claim: a path
// the recorder never saw produces no crash states, and "the sweep found
// nothing" then reads exactly like "the sweep never looked". Code that reaches
// the filesystem through the os package instead of the supplied driver is
// invisible in precisely this way.
func ExampleRecorder_Observed() {
	dir, err := os.MkdirTemp("", "crash-example")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	rec := crash.Record(faultfs.OS(), dir)

	f, err := rec.OpenFile(filepath.Join(dir, "snapshot"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		panic(err)
	}
	if _, err := f.Write([]byte("published")); err != nil {
		panic(err)
	}
	_ = f.Close()

	for _, p := range rec.Observed() {
		fmt.Println("served:", filepath.Base(p))
	}

	// Output:
	// served: snapshot
}
