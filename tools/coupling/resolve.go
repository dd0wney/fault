package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
)

// resolve locates each coupling in the source tree under root.
//
// TODO(ddowney): the resolution.
//
// For each coupling, find the file in c.Package that declares c.Symbol, and the
// line range of its body. Then, if c.Lines is set, narrow the range to it.
//
// Use go/parser, not a regular expression. The bash original scanned for a line
// matching "^func ... Name(" and then the next line that was exactly "}", which
// misses anything gofmt lays out differently and reports the miss as a registry
// error against correct code. parser.ParseDir gives exact positions through the
// token.FileSet, and it costs no dependency.
//
// Three decisions live here.
//
//  1. What counts as a declaration of c.Symbol? A top-level func is obvious.
//     A method with that name is the interesting case -- graphdb's registry
//     names methods, and ast.FuncDecl.Recv is how you tell them apart. Decide
//     whether "Bytes" should match one method, every method of that name, or
//     only a plain function.
//
//  2. What does a symbol that resolves to nothing mean? Section 3 of the spec
//     says refusal, not skip: a renamed function silently dropped would make
//     the tool report a number for fewer couplings than it claims. Return an
//     error naming the coupling.
//
//  3. Which path goes in Site.File? It has to match what the coverage profile
//     writes, and a profile uses the package's import path plus the file name,
//     not an absolute path. Getting this wrong is how the bash version matched
//     pkg/storage2 when the registry said pkg/storage -- so compare the whole
//     thing, never a substring.
//
// Test files are never couplings. Skip anything ending in _test.go.
func resolve(root string, couplings []Coupling) ([]Site, error) {
	_ = parser.ParseDir
	_ = ast.Print
	_ = token.NewFileSet
	_ = os.ReadDir
	_ = filepath.Join
	return nil, fmt.Errorf("resolve: %w", errNotWritten)
}
