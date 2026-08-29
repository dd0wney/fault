package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// --- The registry ---

func TestParseRegistryReadsRows(t *testing.T) {
	f, err := os.Open("testdata/registry.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := parseRegistry(f)
	if err != nil {
		t.Fatalf("parseRegistry: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d couplings, want 2", len(got))
	}
	if got[0].ID != "C1" || got[0].Kind != "control" || got[0].Symbol != "Load" {
		t.Errorf("first row = %+v", got[0])
	}
	if got[1].Kind != "data" || got[1].Symbol != "Save" {
		t.Errorf("second row = %+v", got[1])
	}
}

// A malformed row is a refusal, not a skip. A registry that silently drops one
// reports a number for fewer couplings than it claims to cover.
func TestParseRegistryRefusesBadRows(t *testing.T) {
	for name, input := range map[string]string{
		"too few fields":   "C1\tcontrol\tfixture\n",
		"unknown kind":     "C1\tstructural\tfixture\tLoad\n",
		"bad line range":   "C1\tcontrol\tfixture\tLoad\tnonsense\n",
		"reversed range":   "C1\tcontrol\tfixture\tLoad\t40-10\n",
		"nothing declared": "# only a comment\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRegistry(strings.NewReader(input)); err == nil {
				t.Error("parseRegistry accepted it, want an error")
			}
		})
	}
}

// --- Resolution ---

// The profile writes an import path plus a file name, so a Site.File must be
// in that form. Comparing anything else, or comparing a substring, is how the
// bash original matched pkg/storage2 for a registry naming pkg/storage.
func TestResolveLocatesEachSymbol(t *testing.T) {
	sites, err := resolve("testdata", []Coupling{
		{ID: "C1", Kind: "control", Package: "fixture", Symbol: "Load"},
		{ID: "C2", Kind: "data", Package: "fixture", Symbol: "Save"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("%d sites, want 2", len(sites))
	}
	for _, s := range sites {
		if s.File != "fixture/store.go" {
			t.Errorf("%s: File = %q, want %q", s.ID, s.File, "fixture/store.go")
		}
		if s.Start <= 0 || s.End < s.Start {
			t.Errorf("%s: range %d-%d is not a range", s.ID, s.Start, s.End)
		}
	}
	// Load is declared before Save, so its range must end before Save's begins.
	if sites[0].End >= sites[1].Start {
		t.Errorf("C1 ends at %d and C2 starts at %d: the ranges overlap",
			sites[0].End, sites[1].Start)
	}
}

// A method must be found as readily as a function. graphdb's registry names
// methods, and a resolver that only walks top-level funcs misses them.
func TestResolveFindsAMethod(t *testing.T) {
	sites, err := resolve("testdata", []Coupling{
		{ID: "C2", Kind: "data", Package: "fixture", Symbol: "Save"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(sites) != 1 {
		t.Fatalf("%d sites, want 1", len(sites))
	}
}

// A renamed function silently dropped would make the tool report a number for
// fewer couplings than it claims. Refuse, and name the coupling.
func TestResolveRefusesAnUnknownSymbol(t *testing.T) {
	_, err := resolve("testdata", []Coupling{
		{ID: "C9", Kind: "control", Package: "fixture", Symbol: "Vanished"},
	})
	if err == nil {
		t.Fatal("resolve accepted a symbol nothing declares, want an error")
	}
	if !strings.Contains(err.Error(), "C9") && !strings.Contains(err.Error(), "Vanished") {
		t.Errorf("err = %v, want it to name the coupling", err)
	}
}

func TestResolveNarrowsToALineRange(t *testing.T) {
	sites, err := resolve("testdata", []Coupling{
		{ID: "C1", Kind: "control", Package: "fixture", Symbol: "Load", Lines: "9-11"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if sites[0].Start != 9 || sites[0].End != 11 {
		t.Errorf("range %d-%d, want 9-11", sites[0].Start, sites[0].End)
	}
}

// --- Measurement ---

func mustResolve(t *testing.T) []Site {
	t.Helper()
	sites, err := resolve("testdata", []Coupling{
		{ID: "C1", Kind: "control", Package: "fixture", Symbol: "Load"},
		{ID: "C2", Kind: "data", Package: "fixture", Symbol: "Save"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return sites
}

func TestMeasureAttributesStatements(t *testing.T) {
	sites := mustResolve(t)
	f, err := os.Open("testdata/profile-mixed.out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := measure(f, sites)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("%d results, want 2", len(got))
	}
	if got[0].Total == 0 || got[0].Covered != got[0].Total {
		t.Errorf("C1: %d/%d, want every statement covered", got[0].Covered, got[0].Total)
	}
	if got[1].Total == 0 || got[1].Covered != 0 {
		t.Errorf("C2: %d/%d, want statements present and none covered", got[1].Covered, got[1].Total)
	}
}

// A site that matched no statement is not a coverage result. Every real
// function has statements, so zero means the profile does not cover that
// package, or resolve found the wrong file. Reporting it as "n/a" beside real
// percentages is how a half-written profile gets read as a finished one.
func TestMeasureRefusesAProfileThatMatchesNothing(t *testing.T) {
	sites := mustResolve(t)
	f, err := os.Open("testdata/profile-other-package.out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	_, err = measure(f, sites)
	if err == nil {
		t.Error("measure accepted a profile covering a different package, want an error")
	}
}

func TestMeasureRefusesAnUnreadableProfile(t *testing.T) {
	sites := mustResolve(t)
	for _, name := range []string{"testdata/profile-empty.out", "testdata/profile-truncated.out"} {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(name)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			_, err = measure(f, sites)
			if err == nil {
				t.Error("measure accepted it, want an error")
			}
		})
	}
}

// --- Exit codes ---

func TestExitCodes(t *testing.T) {
	for _, tc := range []struct {
		name, registry, profile string
		want                    int
	}{
		{"every coupling covered", "testdata/registry.tsv", "testdata/profile-all-covered.out", exitCovered},
		{"one coupling uncovered", "testdata/registry.tsv", "testdata/profile-mixed.out", exitUncovered},
		{"a symbol nothing declares", "testdata/registry-unknown-symbol.tsv", "testdata/profile-mixed.out", exitCannotRun},
		{"a registry declaring nothing", "testdata/registry-comments-only.tsv", "testdata/profile-mixed.out", exitCannotRun},
		{"an empty profile", "testdata/registry.tsv", "testdata/profile-empty.out", exitCannotRun},
		{"no registry at all", "testdata/nope.tsv", "testdata/profile-mixed.out", exitCannotRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := run("testdata", tc.registry, tc.profile, "text", io.Discard, io.Discard); got != tc.want {
				t.Errorf("exit %d, want %d", got, tc.want)
			}
		})
	}
}

// --- Registry completeness ---
//
// The registry is DATA the gate reads to decide what to look at, and data
// drifts from the thing it describes. A package added and not declared escapes
// the measure entirely while the gate reports a pass.

func TestCheckCompleteRefusesAnUndeclaredPackage(t *testing.T) {
	err := checkComplete("testdata/mod", []Coupling{
		{ID: "C1", Kind: "control", Package: "example.com/mod/a", Symbol: "A"},
	})
	if err == nil {
		t.Fatal("checkComplete accepted a module whose package b is undeclared")
	}
	if !strings.Contains(err.Error(), "example.com/mod/b") {
		t.Errorf("the diagnostic must name the package a reader has to act on: %v", err)
	}
}

func TestAnExemptionSatisfiesCompleteness(t *testing.T) {
	err := checkComplete("testdata/mod", []Coupling{
		{ID: "C1", Kind: "control", Package: "example.com/mod/a", Symbol: "A"},
		{ID: "X1", Kind: "exempt", Package: "example.com/mod/b", Symbol: "-", Note: "no boundary here"},
	})
	if err != nil {
		t.Errorf("checkComplete = %v, want nil: an exemption is a declaration", err)
	}
}

// An exemption is a claim, and a claim with no reason is a silence with extra
// steps. parseRegistry refuses it, so a reviewer sees the reason in the diff
// that introduces the exemption.
func TestAnExemptionNeedsAReason(t *testing.T) {
	_, err := parseRegistry(strings.NewReader("X1\texempt\texample.com/mod/b\t-\t\t\n"))
	if err == nil {
		t.Error("parseRegistry accepted an exemption with no reason")
	}
}

// A regression test for a defect in this file: filepath.WalkDir yields the root
// first with d.Name() == "." for a relative root, and a dotted-directory skip
// matched it -- ending the walk before it started. The check then reported a
// pass having examined nothing, which is the exact failure this tool exists to
// find.
//
// The tell was that it passed on a registry with a package deliberately
// removed. A completeness check that never refuses is not one.
func TestCheckCompleteRefusesWhenItExaminesNothing(t *testing.T) {
	// A directory with no Go packages under it at all.
	empty := t.TempDir()
	if err := checkComplete(empty, []Coupling{
		{ID: "C1", Kind: "control", Package: "whatever", Symbol: "A"},
	}); err == nil {
		t.Error("checkComplete accepted a tree with no packages, want a refusal")
	}

	// And the case that hid the bug: a relative root of ".".
	//
	// Asserting only that an error came back is not enough, and the first
	// version of this test did exactly that -- so it passed with the bug
	// reintroduced, because the visited==0 guard produced a DIFFERENT error and
	// "an error" satisfied it. The message has to say which failure this is.
	if err := os.Chdir("testdata/mod"); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir("../..")

	err := checkComplete(".", []Coupling{
		{ID: "C1", Kind: "control", Package: "example.com/mod/a", Symbol: "A"},
	})
	if err == nil {
		t.Fatal("with root \".\", checkComplete accepted an undeclared package")
	}
	if !strings.Contains(err.Error(), "example.com/mod/b") {
		t.Errorf("with root \".\", the walk was skipped: it should have found package b "+
			"undeclared, and instead reported %v", err)
	}
}
