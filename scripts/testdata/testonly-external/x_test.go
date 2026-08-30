package x

import (
	"testing"

	// The whole point of the fixture. This import exists only in a test file,
	// and it is the shape the gate could not see.
	"example.com/ext"
)

func TestUpper(t *testing.T) {
	if Upper(ext.Name()) != "EXT" {
		t.Fatal("fixture is wrong")
	}
}
