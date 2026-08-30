package crash

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadTreeThenWriteToRoundTrips(t *testing.T) {
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readTree(src)
	if err != nil {
		t.Fatalf("readTree: %v", err)
	}

	dst := t.TempDir()
	if err := got.writeTo(dst); err != nil {
		t.Fatalf("writeTo: %v", err)
	}
	back, err := readTree(dst)
	if err != nil {
		t.Fatalf("readTree of the copy: %v", err)
	}
	if d := diffTree(got, back); d != "" {
		t.Errorf("a tree did not survive a round trip:\n%s", d)
	}
}

// diffTree is the instrument the positive control depends on. If it cannot
// report a difference, the control passes against a broken replay and every
// finding after it is a fiction.
func TestDiffTreeReportsEachKindOfDifference(t *testing.T) {
	base := tree{"a": {data: []byte("x")}}

	cases := []struct {
		name string
		got  tree
	}{
		{"a missing file", tree{}},
		{"an extra file", tree{"a": {data: []byte("x")}, "b": {data: []byte("y")}}},
		{"different contents", tree{"a": {data: []byte("z")}}},
		{"a file that became a directory", tree{"a": {dir: true}}},
	}
	for _, c := range cases {
		if diffTree(base, c.got) == "" {
			t.Errorf("%s: diffTree reported no difference, so it cannot report the negative", c.name)
		}
	}

	if d := diffTree(base, tree{"a": {data: []byte("x")}}); d != "" {
		t.Errorf("two equal trees differ: %s", d)
	}
}
