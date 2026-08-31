package fs_test

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dd0wney/fault"
	faultfs "github.com/dd0wney/fault/fs"
)

// A leak check needs three readings, not one, and this is what each answers.
//
// Outstanding alone cannot tell a released handle from one that was never
// taken: both report zero. That matters because a sweep over code holding no
// handle would then report a clean leak check having compared zero against
// zero.
func ExampleFault_OpenPaths() {
	dir, err := os.MkdirTemp("", "fault-example")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// A zero Points arms nothing, so every operation below succeeds and the
	// wrapper is transparent. Sweep supplies an armed one.
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())

	a, err := fsys.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		panic(err)
	}
	b, err := fsys.OpenFile(filepath.Join(dir, "b"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		panic(err)
	}

	names := make([]string, 0, 2)
	for _, p := range fsys.OpenPaths() {
		names = append(names, filepath.Base(p))
	}
	fmt.Println("while open: ", fsys.Outstanding(), names)

	_ = a.Close()
	_ = b.Close()

	fmt.Println("after close:", fsys.Outstanding(), fsys.OpenPaths())
	fmt.Println("high water: ", fsys.MaxOutstanding())

	// Output:
	// while open:  2 [a b]
	// after close: 0 []
	// high water:  2
}

// The wrapper serves the real filesystem for every operation the sweep does not
// fail, so the code under test exercises its real path.
func ExampleOS() {
	dir, err := os.MkdirTemp("", "fault-example")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	f, err := faultfs.OS().OpenFile(filepath.Join(dir, "note"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		panic(err)
	}
	n, err := f.Write([]byte("written for real"))
	_ = f.Close()

	fmt.Println(n, err)
	// Output: 16 <nil>
}
