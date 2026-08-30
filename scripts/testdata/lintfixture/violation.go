// Package lintfixture holds one deliberate lint violation.
//
// The lint gate's selftest runs golangci-lint against this module and
// asserts that it fails. A gate that cannot report a violation and a gate
// with nothing to report print the same thing, so this package exists to
// tell them apart.
package lintfixture

import "os"

// Remove discards the error that os.Remove can return. errcheck, part of
// this repository's default linter set, must reject this.
func Remove(path string) {
	os.Remove(path)
}
