// Package config reads a limit from a file, and answers a default when it
// cannot.
package config

import (
	"os"
	"path/filepath"
	"strconv"

	faultfs "github.com/dd0wney/fault/fs"
)

// Limit reads the write limit from dir/limit, and answers 10 when the file
// cannot be opened or read. The default is the coupling app depends on: it
// caps what app hands to store, and store never checks it.
//
// The registry names the default's own line as the site. In the normal range
// run the file does not exist, so the default is the path that runs, and the
// site reads as covered. A sweep that fails the open reaches the same line.
// So the site is 100% in the merged profile and 0% in the robustness-only
// one, which is the reading the chain exists to separate.
func Limit(fsys faultfs.FS, dir string) int {
	f, err := fsys.OpenFile(filepath.Join(dir, "limit"), os.O_RDONLY, 0)
	if err != nil {
		return 10
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 8)
	n, err := f.Read(buf)
	if err != nil {
		return 10
	}
	v, err := strconv.Atoi(string(buf[:n]))
	if err != nil {
		return 10
	}
	return v
}
