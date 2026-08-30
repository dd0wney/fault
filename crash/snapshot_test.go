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

// levels turns one MkdirAll path into the levels that call creates, outermost
// first, so the record can hold one entry for each. It lives with the recorder
// and is tested here because it is a path rule, like the rest of this file.
func TestLevelsListsEveryDirectoryFromTheOutermost(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a", []string{"a"}},
		{"a/b", []string{"a", "a/b"}},
		{"a/b/c", []string{"a", "a/b", "a/b/c"}},
		// The record root is not a key in the tree, and nothing this package
		// records can create it, so it yields no level at all.
		{".", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := levels(c.in)
		if len(got) != len(c.want) {
			t.Errorf("levels(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("levels(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
}
