// Package store writes a file atomically: a temporary, a write, a sync, a
// close, and a rename into place.
package store

import (
	"fmt"
	"os"

	faultfs "github.com/dd0wney/fault/fs"
)

// Write saves data to name through fsys. Every error path removes the
// temporary, so a failed save leaves either the old file or none, and never
// a partial one.
//
// The registry names the whole function as the site. The normal range run
// executes the happy path only. Each error path executes only on the sweep
// pass that fails its operation, so those statements are what the
// robustness-only profile holds and the normal one does not.
func Write(fsys faultfs.FS, name string, data []byte) error {
	tmp := name + ".tmp"
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = fsys.Remove(tmp)
		return fmt.Errorf("write: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = fsys.Remove(tmp)
		return fmt.Errorf("sync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = fsys.Remove(tmp)
		return fmt.Errorf("close: %w", err)
	}
	if err := fsys.Rename(tmp, name); err != nil {
		_ = fsys.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
