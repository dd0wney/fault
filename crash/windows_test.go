//go:build !windows

package crash_test

import "testing"

// The POSIX metadata rule cannot hold on Windows, where a directory handle
// cannot be synced: syncDir returns an error there, so a store that obeys
// POSIX cannot obey it. Every test that calls syncDir therefore builds only
// off Windows. The MetadataDurable path runs everywhere, and it lives in
// crash_test.go with no build tag.
//
// CI decides whether this reasoning is right. A local gate says nothing about
// Windows.
func TestTheDirectorySyncPathIsBuiltHere(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// The presence of this file in the build is the assertion. It exists so
	// that the build tag has a stated reason next to it rather than sitting
	// alone at the top of a test file.
}
