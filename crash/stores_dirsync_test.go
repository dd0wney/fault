//go:build !windows

// safeStore and noFileSync both call syncDir to make a rename durable, and
// syncDir opens the directory and syncs the handle. That call fails on
// Windows, where a directory handle cannot be synced, so both stores and
// syncDir itself build only off Windows. noDirSync and inPlaceStore need no
// directory sync and stay in stores_test.go, unconstrained.

package crash_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// safeStore writes a temporary file, syncs it, renames it into place, and
// syncs the directory. It must survive every state under both models.
func safeStore(fsys faultfs.FS, dir, value string) error {
	tmp := filepath.Join(dir, "data.tmp")
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(value)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, filepath.Join(dir, "data")); err != nil {
		return err
	}
	return syncDir(fsys, dir)
}

// noFileSync never syncs the temporary file, so the rename can publish a name
// whose contents never reached the disk.
func noFileSync(fsys faultfs.FS, dir, value string) error {
	tmp := filepath.Join(dir, "data.tmp")
	f, err := fsys.OpenFile(tmp, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(value)); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := fsys.Rename(tmp, filepath.Join(dir, "data")); err != nil {
		return err
	}
	return syncDir(fsys, dir)
}

// syncDir is how a Go program makes a rename durable: open the directory and
// sync the handle. It fails on Windows, which is why Model.MetadataDurable
// exists, and the error travels back through save to a t.Fatalf rather than
// being swallowed. Task 14 splits the build for that platform.
func syncDir(fsys faultfs.FS, dir string) error {
	d, err := fsys.OpenFile(dir, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		return err
	}
	return d.Close()
}

// TestTheFourReferenceStoresThatSyncADirectory is the other half of the
// acceptance test in crash_test.go: the two stores that call syncDir to make
// their rename durable. That call fails on Windows, so this half builds
// everywhere except there. TestTheFourReferenceStores carries the two stores
// that need no directory sync, and runs on every platform including Windows.
func TestTheFourReferenceStoresThatSyncADirectory(t *testing.T) {
	runReferenceStoreCases(t, []refStoreCase{
		{"safeStore/posix", safeStore, crash.Model{}, true},
		{"safeStore/metadataDurable", safeStore, crash.Model{MetadataDurable: true}, true},
		{"noFileSync/posix", noFileSync, crash.Model{}, false},
		{"noFileSync/metadataDurable", noFileSync, crash.Model{MetadataDurable: true}, false},
	})
}
