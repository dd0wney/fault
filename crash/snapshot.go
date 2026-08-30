package crash

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// node is one name in a tree. A directory carries no data.
type node struct {
	dir  bool
	data []byte
	mode os.FileMode
}

// tree is a directory's whole content, keyed by slash-separated relative path.
// The root itself is not a key.
type tree map[string]node

// readTree copies every name under root into a tree, so the copy can outlive
// changes made to root afterwards. It is what Record takes its snapshot with.
func readTree(root string) (tree, error) {
	t := tree{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		key := filepath.ToSlash(rel)
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		if d.IsDir() {
			t[key] = node{dir: true, mode: info.Mode().Perm()}
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		t[key] = node{data: data, mode: info.Mode().Perm()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// clone returns a copy of t whose byte slices share no backing array with the
// original, so a caller cannot mutate the snapshot through a node it read.
func (t tree) clone() tree {
	out := make(tree, len(t))
	for k, v := range t {
		data := append([]byte(nil), v.data...)
		out[k] = node{dir: v.dir, data: data, mode: v.mode}
	}
	return out
}

// writeTo materialises the tree under dir. Directories are created before the
// files inside them, which sorting the keys guarantees.
func (t tree) writeTo(dir string) error {
	for _, k := range t.keys() {
		n := t[k]
		p := filepath.Join(dir, filepath.FromSlash(k))
		if n.dir {
			if err := os.MkdirAll(p, perm(n.mode, 0o700)); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(p, n.data, perm(n.mode, 0o600)); err != nil {
			return err
		}
	}
	return nil
}

// perm supplies a default when a node carries no mode, which happens for a
// tree built by the replay rather than read from a disk.
func perm(m, def os.FileMode) os.FileMode {
	if m == 0 {
		return def
	}
	return m
}

// keys returns the paths in a stable order. Go randomises map iteration on
// purpose, so anything that reaches a state name or a comparison must sort.
func (t tree) keys() []string {
	out := make([]string, 0, len(t))
	for k := range t {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// diffTree describes how got differs from want, and returns "" when they are
// identical. It must be able to report every kind of difference, because the
// positive control believes it: task 6 asserts that replaying the whole
// record reproduces the directory the scenario actually wrote, and this
// function is what decides whether that assertion is true.
func diffTree(want, got tree) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, k := range want.keys() {
		seen[k] = true
		w := want[k]
		g, ok := got[k]
		switch {
		case !ok:
			fmt.Fprintf(&b, "missing: %s\n", k)
		case w.dir != g.dir:
			fmt.Fprintf(&b, "kind differs: %s (want dir=%v, got dir=%v)\n", k, w.dir, g.dir)
		case !w.dir && !bytes.Equal(w.data, g.data):
			fmt.Fprintf(&b, "contents differ: %s (want %d bytes, got %d bytes)\n", k, len(w.data), len(g.data))
		}
	}
	for _, k := range got.keys() {
		if !seen[k] {
			fmt.Fprintf(&b, "unexpected: %s\n", k)
		}
	}
	return b.String()
}
