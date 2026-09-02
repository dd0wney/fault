package main

import (
	"bytes"
	"errors"
	"go/ast"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file each kill a survivor the ./coupling/ baseline row
// recorded as a real gap. All 193 survivors of the run at 201c59a were read
// on 2026-09-02: 90 are killed by pins_test.go, 22 are equivalent and named
// in the baseline row, and the 81 real gaps are here, grouped by the fixture
// that kills several at once.

// writeTree writes files under root, creating directories as needed. A key
// ending in "/" makes an empty directory.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if strings.HasSuffix(name, "/") {
			if err := os.MkdirAll(p, 0o750); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// failAfter yields its text and then fails, so a scanner sees content before
// the error. A reader that fails at once never reaches the loop that reads
// sc.Err().
type failAfter struct {
	r   io.Reader
	err error
}

func (f *failAfter) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, f.err
	}
	return n, err
}

var errRead = errors.New("the reader failed")

// --- checkComplete ---

func TestCheckCompleteRefusesARootThatDoesNotExist(t *testing.T) {
	err := checkComplete(filepath.Join(t.TempDir(), "no-such-root"), []Coupling{{ID: "C1", Kind: "control", Package: "x", Symbol: "A"}})
	if err == nil {
		t.Fatal("checkComplete accepted a root that does not exist")
	}
	if !strings.Contains(err.Error(), "walking") {
		t.Errorf("the refusal must come from the walk, not from the empty-visit guard: %v", err)
	}
}

// The named directories and the dot directories are skipped, so a Go file
// inside one is not a package the registry must declare.
func TestCheckCompleteSkipsTheNamedAndDottedDirectories(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":            "module x\n",
		"sub/a.go":          "package sub\n",
		"testdata/a.go":     "package t\n",
		".git/a.go":         "package g\n",
		"docs/a.go":         "package d\n",
		"coverage/a.go":     "package c\n",
		".hidden/a.go":      "package h\n",
		"nested/.also/a.go": "package n\n",
		"nested/b.go":       "package nested\n",
	})
	err := checkComplete(root, []Coupling{
		{ID: "C1", Kind: "control", Package: "x/sub", Symbol: "A"},
		{ID: "C2", Kind: "control", Package: "x/nested", Symbol: "B"},
	})
	if err != nil {
		t.Errorf("checkComplete = %v, want nil: every skipped directory was reported as a package", err)
	}
}

// A dot-prefixed FILE is not a directory to skip. Returning SkipDir for a file
// skips the rest of its directory, and the undeclared package beside it then
// goes unreported.
func TestCheckCompleteDoesNotStopTheWalkAtADotFile(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":    "module x\n",
		".keep":     "",
		"real/a.go": "package real\n",
		"zz/a.go":   "package zz\n",
	})
	err := checkComplete(root, []Coupling{{ID: "C1", Kind: "control", Package: "x/real", Symbol: "A"}})
	if err == nil || !strings.Contains(err.Error(), "x/zz") {
		t.Errorf("checkComplete = %v, want the undeclared package x/zz named", err)
	}
}

// A directory with only a test file, or only a subdirectory whose name ends
// in .go, holds no Go source and is not a package.
func TestHasGoFilesIgnoresTestOnlyAndDotGoDirectories(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":                "module x\n",
		"real/a.go":             "package real\n",
		"testonly/a_test.go":    "package testonly\n",
		"weird/sub.go/":         "",
		"weird/sub.go/keep.txt": "",
	})
	err := checkComplete(root, []Coupling{{ID: "C1", Kind: "control", Package: "x/real", Symbol: "A"}})
	if err != nil {
		t.Errorf("checkComplete = %v, want nil: a test-only directory or a .go-named directory is not a package", err)
	}
}

// The missing packages are named in sorted order, not in walk order. The walk
// is depth-first, so x/a/sub is visited before x/a-tools, and '-' sorts
// before '/'.
func TestCheckCompleteSortsTheMissingPackages(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"go.mod":        "module x\n",
		"a/sub/a.go":    "package sub\n",
		"a-tools/a.go":  "package tools\n",
		"declared/a.go": "package declared\n",
	})
	err := checkComplete(root, []Coupling{{ID: "C1", Kind: "control", Package: "x/declared", Symbol: "A"}})
	if err == nil {
		t.Fatal("checkComplete accepted two undeclared packages")
	}
	msg := err.Error()
	i, j := strings.Index(msg, "x/a-tools"), strings.Index(msg, "x/a/sub")
	if i < 0 || j < 0 || i > j {
		t.Errorf("the missing packages must be sorted, x/a-tools before x/a/sub: %v", err)
	}
}

// --- parseRegistry and parseLineRange ---

func TestParseRegistryReadsTheLinesAndTheNote(t *testing.T) {
	got, err := parseRegistry(strings.NewReader("C1\tcontrol\tpkg\tSym\t9-11\tthe note text\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Lines != "9-11" || got[0].Note != "the note text" {
		t.Errorf("Lines %q, Note %q; want 9-11 and the note text", got[0].Lines, got[0].Note)
	}
}

func TestParseRegistryAcceptsAFourFieldRow(t *testing.T) {
	got, err := parseRegistry(strings.NewReader("C1\tcontrol\tfixture\tLoad\n"))
	if err != nil {
		t.Fatalf("a four-field row is the minimum and was refused: %v", err)
	}
	if got[0].Lines != "" || got[0].Note != "" {
		t.Errorf("a four-field row carries Lines %q and Note %q, want neither", got[0].Lines, got[0].Note)
	}
}

func TestParseRegistryNamesTheLineOfABadRow(t *testing.T) {
	_, err := parseRegistry(strings.NewReader("# a comment\nC1\tbad\tpkg\tSym\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2:") {
		t.Errorf("parseRegistry = %v, want the refusal to name line 2", err)
	}
}

func TestParseRegistryCarriesAReadError(t *testing.T) {
	_, err := parseRegistry(&failAfter{strings.NewReader("C1\tcontrol\tpkg\tSym\n"), errRead})
	if !errors.Is(err, errRead) {
		t.Errorf("parseRegistry = %v, want the reader's own error", err)
	}
}

func TestParseRegistrySkipsABlankLineBetweenRows(t *testing.T) {
	got, err := parseRegistry(strings.NewReader("C1\tcontrol\tpkg\tA\n\nC2\tdata\tpkg\tB\n"))
	if err != nil {
		t.Fatalf("a blank line between rows was refused: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("%d couplings, want 2", len(got))
	}
}

func TestParseLineRangeRefusesEachBadShapeAndAcceptsOneLine(t *testing.T) {
	for _, bad := range []string{"nonsense", "abc-10", "0-xyz", "12-9"} {
		if _, _, err := parseLineRange(bad); err == nil {
			t.Errorf("parseLineRange(%q) accepted it", bad)
		}
	}
	if _, _, err := parseLineRange("nonsense"); err == nil || !strings.Contains(err.Error(), `want "start-end"`) {
		t.Errorf("the no-dash refusal must say the shape wanted: %v", err)
	}
	start, end, err := parseLineRange("9-9")
	if err != nil || start != 9 || end != 9 {
		t.Errorf("parseLineRange(9-9) = %d, %d, %v; want 9, 9, nil: a one-line range is a range", start, end, err)
	}
}

// --- the command ---

// The exit codes are the documented numbers, compared as numbers. Every other
// test compares against the constants by name, so a constant that changed
// value moved both sides together.
func TestTheExitCodesAreTheDocumentedNumbers(t *testing.T) {
	if exitCovered != 0 || exitUncovered != 1 || exitCannotRun != 2 {
		t.Errorf("exit codes %d, %d, %d; want 0, 1, 2", exitCovered, exitUncovered, exitCannotRun)
	}
}

// Every failure stage of run names its cause on stderr. TestExitCodes sends
// stderr to io.Discard, so every message could go missing with the exit code
// unchanged.
func TestRunNamesEachFailureOnStderr(t *testing.T) {
	dir := t.TempDir()
	registryUnknown := filepath.Join(dir, "unknown.tsv")
	if err := os.WriteFile(registryUnknown, []byte("C1\tcontrol\tfixture\tNoSuchSymbol\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registryOnlyA := filepath.Join(dir, "only-a.tsv")
	if err := os.WriteFile(registryOnlyA, []byte("C1\tcontrol\texample.com/mod/a\tA\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, root, registry, profile, wants string }{
		// "open " and the path, not the path alone: with the refusal gone the
		// nil handle reaches parseRegistry, and THAT message names the path
		// too. The first version of this row asserted the path alone and let
		// the mutant live.
		{"a missing registry", "testdata", filepath.Join(dir, "nope.tsv"), "testdata/profile-mixed.out", "open " + filepath.Join(dir, "nope.tsv")},
		{"a registry declaring nothing", "testdata", "testdata/registry-comments-only.tsv", "testdata/profile-mixed.out", "declares no couplings"},
		{"an undeclared package", "testdata/mod", registryOnlyA, "testdata/profile-mixed.out", "example.com/mod/b"},
		{"a symbol nothing declares", "testdata", registryUnknown, "testdata/profile-mixed.out", "the registry and the code disagree"},
		{"a missing profile", "testdata", "testdata/registry.tsv", filepath.Join(dir, "nope.out"), "open " + filepath.Join(dir, "nope.out")},
		{"a missing profile, the hint", "testdata", "testdata/registry.tsv", filepath.Join(dir, "nope.out"), "-coverprofile"},
		{"a profile that matches nothing", "testdata", "testdata/registry.tsv", "testdata/profile-other-package.out", "matched no statement"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tc.root, tc.registry, tc.profile, "text", &stdout, &stderr); got != exitCannotRun {
				t.Fatalf("exit %d, want %d", got, exitCannotRun)
			}
			if !strings.Contains(stderr.String(), tc.wants) {
				t.Errorf("stderr lacks %q: %q", tc.wants, stderr.String())
			}
		})
	}
}

// main wires the flags to run and run's code to the process exit. This builds
// the command and runs it twice, because no test ran the process.
func TestTheCommandWiresFlagsToRunAndRunToTheExit(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "coupling")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "-root", "testdata", "-registry", "testdata/registry.tsv", "-profile", "testdata/profile-mixed.out")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Errorf("the mixed profile gave %v, want exit 1 with one site uncovered", err)
	}
	if !strings.Contains(string(out), "coupling site") {
		t.Errorf("the command printed no report:\n%s", out)
	}

	err = exec.Command(bin, "-root", "testdata", "-registry", "testdata/nope.tsv", "-profile", "testdata/profile-mixed.out").Run()
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Errorf("a missing registry gave %v, want exit 2", err)
	}
}

// --- measure ---

func twoSites() []Site {
	return []Site{
		{Coupling: Coupling{ID: "X", Package: "p", Symbol: "X"}, File: "x.go", Start: 10, End: 20},
		{Coupling: Coupling{ID: "Y", Package: "p", Symbol: "Y"}, File: "y.go", Start: 10, End: 20},
	}
}

func TestMeasureSkipsABlankLineAndReadsPastIt(t *testing.T) {
	got, err := measure(strings.NewReader("mode: set\nx.go:10.1,10.5 1 1\n\ny.go:10.1,10.5 1 1\n"), twoSites())
	if err != nil {
		t.Fatalf("measure refused a profile with a blank line: %v", err)
	}
	if got[1].Total != 1 {
		t.Errorf("Y counted %d statements, want 1: the block after the blank line was dropped", got[1].Total)
	}
}

func TestMeasureRefusesABlockBeforeTheHeaderEvenWhenOneFollows(t *testing.T) {
	_, err := measure(strings.NewReader("x.go:10.1,10.5 1 1\nmode: set\ny.go:10.1,10.5 1 1\n"), twoSites())
	if err == nil {
		t.Fatal("measure accepted a block that came before the mode header")
	}
}

// The disagreement refusal must fire on its own, with every site covered, or
// the unmatched-site refusal masks it.
func TestMeasureRefusesADisagreeingDuplicateWhenEverySiteIsCovered(t *testing.T) {
	_, err := measure(strings.NewReader("mode: set\nx.go:10.1,10.5 1 1\ny.go:10.1,10.5 1 1\nx.go:10.1,10.5 2 1\n"), twoSites())
	if err == nil || !strings.Contains(err.Error(), "statements") {
		t.Errorf("measure = %v, want the statement-count disagreement refusal", err)
	}
}

func TestMeasureCarriesAReadError(t *testing.T) {
	_, err := measure(&failAfter{strings.NewReader("mode: set\nx.go:10.1,10.5 1 1\ny.go:10.1,10.5 1 1\n"), errRead}, twoSites())
	if !errors.Is(err, errRead) {
		t.Errorf("measure = %v, want the reader's own error", err)
	}
}

func TestMeasureNamesTheMissingHeaderForAnEmptyProfile(t *testing.T) {
	_, err := measure(strings.NewReader(""), twoSites())
	if err == nil || !strings.Contains(err.Error(), "mode:") {
		t.Errorf("measure = %v, want the missing-header refusal, not the unmatched-site one", err)
	}
}

// A block in another file with the same lines does not count, even when
// every site has a real block too, so the unmatched refusal cannot mask it.
func TestMeasureIgnoresABlockInAnotherFileWithTheSameLines(t *testing.T) {
	got, err := measure(strings.NewReader("mode: set\nx.go:10.1,10.5 1 1\ny.go:10.1,10.5 1 1\nz.go:10.1,10.5 5 1\nz.go:15.1,15.5 5 1\n"), twoSites())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Total != 1 || got[1].Total != 1 {
		t.Errorf("X %d and Y %d statements, want 1 each: a block in z.go was attributed by its lines", got[0].Total, got[1].Total)
	}
}

// A block with no file name before the colon is malformed, and so is one with
// no colon. Both are refused, and neither panics.
func TestParseBlockRefusesAnEmptyFileName(t *testing.T) {
	for _, line := range []string{":8.30,9.18 1 1", "noColonHere 1 1"} {
		if _, _, _, _, _, err := parseBlock(line); err == nil {
			t.Errorf("parseBlock(%q) accepted a block with no file name", line)
		}
	}
}

// --- resolve ---

// fixturePackage writes a module with one package under root and returns
// the root.
func fixturePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	all := map[string]string{"go.mod": "module x\n"}
	for k, v := range files {
		all["p/"+k] = v
	}
	writeTree(t, root, all)
	return root
}

func resolveOne(t *testing.T, root string, c Coupling) ([]Site, error) {
	t.Helper()
	c.Package = "x/p"
	if c.Kind == "" {
		c.Kind = "control"
	}
	if c.ID == "" {
		c.ID = "C1"
	}
	return resolve(root, []Coupling{c})
}

func TestResolveRefusesAnAmbiguousUnqualifiedSymbol(t *testing.T) {
	root := fixturePackage(t, map[string]string{
		"p.go": "package p\ntype A struct{}\ntype B struct{}\nfunc (A) Do() {}\nfunc (B) Do() {}\n",
	})
	_, err := resolveOne(t, root, Coupling{Symbol: "Do"})
	if err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Errorf("resolve = %v, want the ambiguity refusal naming 2 declarations", err)
	}
}

func TestResolveFindsAQualifiedMethod(t *testing.T) {
	sites, err := resolve("testdata", []Coupling{{ID: "C1", Kind: "data", Package: "fixture", Symbol: "Store.Save"}})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sites[0].Start != 19 {
		t.Errorf("Store.Save resolved to line %d, want 19", sites[0].Start)
	}
}

func TestResolveFindsAMethodOnAGenericReceiver(t *testing.T) {
	root := fixturePackage(t, map[string]string{
		"p.go": "package p\ntype G[T any] struct{}\nfunc (g *G[T]) Do() {}\ntype H[K comparable, V any] struct{}\nfunc (h H[K, V]) Do() {}\n",
	})
	for _, sym := range []string{"G.Do", "H.Do"} {
		if _, err := resolveOne(t, root, Coupling{Symbol: sym}); err != nil {
			t.Errorf("resolve(%s) = %v, want the generic receiver unwrapped", sym, err)
		}
	}
}

func TestReceiverNameAnswersNothingForANonReceiver(t *testing.T) {
	if got := receiverName(&ast.SelectorExpr{X: ast.NewIdent("a"), Sel: ast.NewIdent("b")}); got != "" {
		t.Errorf("receiverName of a selector = %q, want empty", got)
	}
}

func TestResolveSkipsAnExemptRowAndContinuesPastIt(t *testing.T) {
	sites, err := resolve("testdata", []Coupling{
		{ID: "X1", Kind: "exempt", Package: "other", Symbol: "-", Note: "no boundary"},
		{ID: "C1", Kind: "control", Package: "fixture", Symbol: "Load"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(sites) != 1 || sites[0].ID != "C1" {
		t.Errorf("resolve gave %d sites, want the one real row after the exempt one", len(sites))
	}
	only, err := resolve("testdata", []Coupling{{ID: "X1", Kind: "exempt", Package: "other", Symbol: "-", Note: "no boundary"}})
	if err != nil || len(only) != 0 {
		t.Errorf("resolve of an exempt row alone = %d sites, %v; want none and no error", len(only), err)
	}
}

func TestResolveNamesAPackageDirectoryItCannotRead(t *testing.T) {
	_, err := resolve("testdata", []Coupling{{ID: "C1", Kind: "control", Package: "no-such-package", Symbol: "A"}})
	if err == nil || !strings.Contains(err.Error(), "names package") {
		t.Errorf("resolve = %v, want the read failure named", err)
	}
}

// Entries that are not Go source are skipped without stopping the scan: a
// subdirectory, a subdirectory named like a Go file, a text file, and a test
// file that declares the same symbol.
func TestResolveSkipsWhatIsNotGoSourceAndKeepsScanning(t *testing.T) {
	root := fixturePackage(t, map[string]string{
		"aaa_test.go": "package p\nfunc A() {}\n",
		"dir/":        "",
		"dir.go/":     "",
		"notes.txt":   "not go",
		"zzz.go":      "package p\nfunc A() {}\n",
	})
	sites, err := resolveOne(t, root, Coupling{Symbol: "A"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(sites) != 1 || !strings.HasSuffix(sites[0].File, "zzz.go") {
		t.Errorf("resolve gave %+v, want the one declaration in zzz.go", sites)
	}
}

func TestResolveRefusesAPackageWithMalformedSource(t *testing.T) {
	root := fixturePackage(t, map[string]string{"p.go": "package p\nfunc A( {\n"})
	if _, err := resolveOne(t, root, Coupling{Symbol: "A"}); err == nil {
		t.Error("resolve accepted a package whose only file does not parse")
	}
}

// The line range narrows within the declaration, inclusive at both ends.
// Load in testdata/fixture is declared at lines 8-16.
func TestResolveNarrowsWithinTheDeclarationInclusive(t *testing.T) {
	for _, tc := range []struct {
		lines string
		ok    bool
		names string
	}{
		{"8-11", true, ""},
		{"10-16", true, ""},
		{"1-100", false, "outside"},
		{"1-11", false, "outside"},
		{"10-100", false, "outside"},
		{"garbage", false, "line range"},
	} {
		t.Run(tc.lines, func(t *testing.T) {
			sites, err := resolve("testdata", []Coupling{{ID: "C1", Kind: "control", Package: "fixture", Symbol: "Load", Lines: tc.lines}})
			if tc.ok {
				if err != nil {
					t.Fatalf("resolve refused %s: %v", tc.lines, err)
				}
				start, end, _ := parseLineRange(tc.lines)
				if sites[0].Start != start || sites[0].End != end {
					t.Errorf("narrowed to %d-%d, want %s", sites[0].Start, sites[0].End, tc.lines)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.names) {
				t.Errorf("resolve = %v, want a refusal naming %q", err, tc.names)
			}
		})
	}
}

// matches must not panic on a receiver list that is present and empty, and a
// qualified name must skip a plain function of the same name.
func TestMatchesTreatsAnEmptyReceiverListAndAPlainFunctionAsNoMatch(t *testing.T) {
	fn := &ast.FuncDecl{Name: ast.NewIdent("Save"), Recv: &ast.FieldList{}}
	if matches(fn, "Store.Save") {
		t.Error("matches reported a method on an empty receiver list")
	}
	root := fixturePackage(t, map[string]string{
		"p.go": "package p\ntype Store struct{}\nfunc Save() {}\nfunc (s *Store) Save() {}\n",
	})
	sites, err := resolveOne(t, root, Coupling{Symbol: "Store.Save"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sites[0].Start != 4 {
		t.Errorf("Store.Save resolved to line %d, want 4, the method and not the plain function", sites[0].Start)
	}
}
