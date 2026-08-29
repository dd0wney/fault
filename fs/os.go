package fs

import "os"

// OS returns an FS backed by the os package. It is what ships, and it is what
// a sweep wraps.
func OS() FS { return osFS{} }

type osFS struct{}

func (osFS) OpenFile(name string, flag int, perm os.FileMode) (File, error) {
	f, err := os.OpenFile(name, flag, perm)
	if err != nil {
		// Return a nil interface rather than a non-nil interface holding a nil
		// *os.File, which would not compare equal to nil at the call site.
		return nil, err
	}
	return f, nil
}

func (osFS) Remove(name string) error                     { return os.Remove(name) }
func (osFS) Rename(oldname, newname string) error         { return os.Rename(oldname, newname) }
func (osFS) Stat(name string) (os.FileInfo, error)        { return os.Stat(name) }
func (osFS) MkdirAll(name string, perm os.FileMode) error { return os.MkdirAll(name, perm) }
func (osFS) ReadDir(name string) ([]os.DirEntry, error)   { return os.ReadDir(name) }
