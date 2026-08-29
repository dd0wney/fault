// Package fs injects filesystem faults into code under test.
//
// It is an adapter over [fault]. The core counts operations and decides which
// one must fail; this package knows what a filesystem operation is, and what
// error a real filesystem would return when one fails.
//
// # Wrap, do not replace
//
// New wraps a real filesystem rather than substituting a fake one. Every
// operation the sweep does not fail is served for real, so the code under test
// writes real bytes to a real disk and the sweep exercises the real path.
// SQLite calls this an overlay, and uses the same structure for its
// out-of-memory tests: the existing allocator is saved by the overlay and used
// as a fallback to do real work.
//
// # Naming
//
// Method names and signatures match [os.Root] wherever the two overlap. The Go
// standard library has no writable filesystem interface -- proposal
// golang/go#45757 asked for one and is closed -- so this package defines its
// own. Matching an accepted shape is the cheapest form of forward
// compatibility available.
//
// A caller that also imports io/fs must alias one of the two:
//
//	import (
//		"io/fs"
//		faultfs "github.com/dd0wney/fault/fs"
//	)
//
// # The adapter contract
//
// The rules every adapter follows -- when to call Trip, what to return, and
// why an operation that skips the call is invisible to the sweep -- live in
// the core package's documentation, under "Writing an adapter":
//
//	go doc github.com/dd0wney/fault
//
// This package takes the one principled exception to rule 2, at Close, and
// says why at that method.
//
// Every Op string here was measured by making the real os package fail and
// reading what it reported. Do not change one from memory.
package fs

import "os"

// FS is the set of filesystem operations this package can fail.
//
// An implementation must be safe for concurrent use.
type FS interface {
	// OpenFile opens a file, with the semantics of os.Root.OpenFile.
	OpenFile(name string, flag int, perm os.FileMode) (File, error)

	// Remove deletes a file, with the semantics of os.Root.Remove.
	Remove(name string) error

	// Rename moves a file, with the semantics of os.Root.Rename.
	//
	// A store that relies on rename being atomic must say so, because an
	// implementation is not required to offer it.
	Rename(oldname, newname string) error

	// Stat returns file metadata, with the semantics of os.Root.Stat.
	Stat(name string) (os.FileInfo, error)

	// MkdirAll creates a directory tree, with the semantics of os.Root.MkdirAll.
	MkdirAll(name string, perm os.FileMode) error

	// ReadDir lists a directory, with the semantics of os.ReadDir. os.Root has
	// no equivalent, so this one follows the os package instead.
	ReadDir(name string) ([]os.DirEntry, error)
}

// File is an open file. The method set matches os.File.
type File interface {
	Read(b []byte) (int, error)
	Write(b []byte) (int, error)
	Sync() error
	Truncate(size int64) error
	Close() error
}
