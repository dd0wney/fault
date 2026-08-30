// Package x imports nothing outside the standard library.
package x

import "strings"

// Upper is here so the package has a statement to compile and a test to call.
func Upper(s string) string { return strings.ToUpper(s) }
