// Package app is the component that couples config to store.
package app

import (
	"fmt"
	"path/filepath"

	faultfs "github.com/dd0wney/fault/fs"

	"demo/config"
	"demo/store"
)

// Save writes data to dir/data, capped at the configured limit.
//
// The registry names the whole function as the site. The normal range run
// executes everything but the error return, and the sweep adds that one
// statement, so the site is partly covered in each column and fully covered
// in neither: the ordinary shape, which the chain reports as the numbers say.
func Save(fsys faultfs.FS, dir string, data []byte) error {
	limit := config.Limit(fsys, dir)
	if len(data) > limit {
		data = data[:limit]
	}
	if err := store.Write(fsys, filepath.Join(dir, "data"), data); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	return nil
}
