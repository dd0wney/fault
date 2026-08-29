package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// resolve locates each coupling in the source tree under root.
//
// It uses go/parser rather than a regular expression. The bash original scanned
// for a line matching "^func ... Name(" and then the next line that was exactly
// "}", which misses anything gofmt lays out differently and reports the miss as
// a registry error against correct code. A FileSet gives exact positions and
// costs no dependency.
//
// Three decisions, each with a reason:
//
//  1. A method counts as a declaration of its name, because registries name
//     methods. Where a name is ambiguous -- a package declaring OpenFile on two
//     types -- the registry writes "Type.Method" to say which. An ambiguity left
//     unresolved is an error rather than a guess: measuring the wrong one
//     silently is the failure this whole tool exists to prevent.
//
//  2. A symbol that resolves to nothing is a refusal, not a skip. A renamed
//     function silently dropped would make the tool report a number for fewer
//     couplings than it claims to cover.
//
//  3. Site.File is the package path plus the file name, which is the form a
//     coverage profile writes. Compare the whole thing and never a substring:
//     the bash version's substring test matched pkg/storage2 for a registry
//     naming pkg/storage.
//
// Test files are never couplings.
func resolve(root string, couplings []Coupling) ([]Site, error) {
	out := make([]Site, 0, len(couplings))

	// A registry names what the coverage profile names, which is an import
	// path. Finding the source needs a directory, so the module path is
	// stripped. A tree with no go.mod -- a fixture -- leaves the two the same.
	module := modulePath(root)

	for _, c := range couplings {
		rel := strings.TrimPrefix(strings.TrimPrefix(c.Package, module), "/")
		if rel == "" {
			rel = "."
		}
		dir := filepath.Join(root, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, fmt.Errorf("%s names package %q: %w", c.ID, c.Package, err)
		}

		fset := token.NewFileSet()
		var (
			site  Site
			found int
		)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}

			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.ID, err)
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || !matches(fn, c.Symbol) {
					continue
				}
				found++
				site = Site{
					Coupling: c,
					File:     path.Join(c.Package, name),
					Start:    fset.Position(fn.Pos()).Line,
					End:      fset.Position(fn.End()).Line,
				}
			}
		}

		switch {
		case found == 0:
			return nil, fmt.Errorf("%s names %s.%s, which no file in that package declares",
				c.ID, c.Package, c.Symbol)
		case found > 1:
			return nil, fmt.Errorf("%s names %s.%s, which that package declares %d times; "+
				"narrow it with a line range rather than measuring an arbitrary one",
				c.ID, c.Package, c.Symbol, found)
		}

		if c.Lines != "" {
			start, end, err := parseLineRange(c.Lines)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", c.ID, err)
			}
			if start < site.Start || end > site.End {
				return nil, fmt.Errorf("%s narrows %s.%s to lines %s, which fall outside the "+
					"declaration at lines %d-%d", c.ID, c.Package, c.Symbol, c.Lines,
					site.Start, site.End)
			}
			site.Start, site.End = start, end
		}

		out = append(out, site)
	}
	return out, nil
}

// modulePath reads the module path from root/go.mod, or returns "" when there
// is none. It parses the one line it needs rather than depending on
// golang.org/x/mod, because this module carries no external dependency.
func modulePath(root string) string {
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// matches reports whether fn declares the registry's symbol.
//
// A bare name matches a function or a method of that name. A "Type.Method" name
// matches only that type's method, which is how a registry disambiguates a
// package declaring the same method on two types.
func matches(fn *ast.FuncDecl, symbol string) bool {
	recv, method, qualified := strings.Cut(symbol, ".")
	if !qualified {
		return fn.Name.Name == symbol
	}
	if fn.Name.Name != method || fn.Recv == nil || len(fn.Recv.List) == 0 {
		return false
	}
	return receiverName(fn.Recv.List[0].Type) == recv
}

// receiverName returns the type name of a receiver, ignoring the pointer and
// any type parameters.
func receiverName(expr ast.Expr) string {
	for {
		switch t := expr.(type) {
		case *ast.StarExpr:
			expr = t.X
		case *ast.IndexExpr:
			expr = t.X
		case *ast.IndexListExpr:
			expr = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}
