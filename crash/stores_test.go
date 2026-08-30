package crash_test

import (
	"io"
	"os"
	"path/filepath"

	faultfs "github.com/dd0wney/fault/fs"
)

// The four reference stores. Two of them must fail, because a sweep that never
// fails anything is indistinguishable from a sweep that works.
//
// Each store publishes one value under dir and returns when it believes the
// value is on the disk. What separates them is only which sync they skip, so
// the table that runs them measures the durability model and nothing else.

// saveFunc is the shape every reference store has.
type saveFunc func(fsys faultfs.FS, dir, value string) error

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

// noDirSync syncs the file but never the directory, so the rename itself can
// be lost. It fails under the POSIX rule and survives under MetadataDurable,
// which makes it the only store that can catch that field being read and then
// ignored.
func noDirSync(fsys faultfs.FS, dir, value string) error {
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
	return fsys.Rename(tmp, filepath.Join(dir, "data"))
}

// inPlaceStore writes straight to the destination. This is the store the
// package documentation names as the worked example, and no returned-error
// sweep catches it.
//
// The name carries the Store suffix because run_test.go already has an inPlace
// helper of its own in this package, which builds a record rather than a
// store.
func inPlaceStore(fsys faultfs.FS, dir, value string) error {
	f, err := fsys.OpenFile(filepath.Join(dir, "data"), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(value)); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
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

// readStore reads back the published value. It is the one observation the
// reference table makes of a rebuilt state.
//
// faultfs.File already declares Read([]byte) (int, error), so it satisfies
// io.Reader with no adapter, and io.ReadAll returns only at EOF or at a real
// error: a short read from the underlying handle is retried rather than
// reported as the whole file.
func readStore(fsys faultfs.FS, dir string) (string, error) {
	f, err := fsys.OpenFile(filepath.Join(dir, "data"), os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
