package main

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// checkComplete refuses when a package in the module appears in neither a
// coupling row nor an exemption.
//
// The registry is DATA the gate reads to decide what to look at, and data
// drifts from the thing it describes. Add a package, and it escapes the measure
// entirely while the gate reports a pass -- having measured exactly what it was
// told to, when what it was told had gone stale.
//
// What this cannot do is find a MISSING coupling inside a package that has
// rows. "What packages exist" is mechanically derivable; "what counts as a
// coupling" is a judgement, and a gate cannot enumerate a judgement. So this
// finds a registry that has stopped being maintained, which is the failure that
// precedes the one you actually fear.
//
// An exemption is a row, not a silence, so a reviewer sees the claim and its
// reason in the diff that introduces it.
func checkComplete(root string, couplings []Coupling) error {
	module := modulePath(root)

	declared := make(map[string]string, len(couplings))
	for _, c := range couplings {
		declared[c.Package] = c.Kind
	}

	var (
		missing []string
		visited int
	)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		// The root itself is never skipped. WalkDir yields it first with
		// d.Name() == "." for a relative root, and the dotted-directory rule
		// below matched it -- which ended the walk before it started and made
		// this check report a pass having examined nothing. That is the exact
		// failure this tool exists to find, in this tool.
		if p != root {
			switch name := d.Name(); {
			case name == "testdata", name == ".git", name == "docs", name == "coverage":
				return filepath.SkipDir
			case strings.HasPrefix(name, "."):
				return filepath.SkipDir
			}
		}
		// A directory holding its own go.mod is a different module, and this
		// gate stops at the module boundary.
		if p != root {
			if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
				return filepath.SkipDir
			}
		}
		if !hasGoFiles(p) {
			return nil
		}

		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		importPath := module
		if rel != "." {
			importPath = path.Join(module, filepath.ToSlash(rel))
		}
		visited++
		if _, ok := declared[importPath]; !ok {
			missing = append(missing, importPath)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walking %s: %w", root, err)
	}

	// A walk that visited no package is not a pass. Every module has at least
	// one, so zero means the walk was stopped rather than satisfied.
	if visited == 0 {
		return fmt.Errorf("found no Go package under %s at all, so this check examined "+
			"nothing -- refusing to report a pass", root)
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("these packages appear in neither a coupling row nor an "+
			"exemption, so the gate would report a pass having measured nothing about "+
			"them: %s. Add a row, or an \"exempt\" row saying why the package has no "+
			"coupling", strings.Join(missing, ", "))
	}
	return nil
}

// hasGoFiles reports whether a directory holds any non-test Go source.
func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			return true
		}
	}
	return false
}
