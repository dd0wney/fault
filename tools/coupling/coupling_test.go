package main

import (
	"errors"
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
	if errors.Is(err, errNotWritten) {
		t.Skip("resolve is not written yet")
	}
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
	if errors.Is(err, errNotWritten) {
		t.Skip("resolve is not written yet")
	}
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
	if errors.Is(err, errNotWritten) {
		t.Skip("resolve is not written yet")
	}
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
	if errors.Is(err, errNotWritten) {
		t.Skip("resolve is not written yet")
	}
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
	if errors.Is(err, errNotWritten) {
		t.Skip("resolve is not written yet")
	}
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
	if errors.Is(err, errNotWritten) {
		t.Skip("measure is not written yet")
	}
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
	if errors.Is(err, errNotWritten) {
		t.Skip("measure is not written yet")
	}
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
			if errors.Is(err, errNotWritten) {
				t.Skip("measure is not written yet")
			}
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
			// Reaching exit 0 or 1 needs a real measurement. The refusal cases
			// below do not, and they are gated already.
			if tc.want != exitCannotRun {
				if _, err := resolve("testdata", nil); errors.Is(err, errNotWritten) {
					t.Skip("resolve is not written yet")
				}
			}
			if got := run("testdata", tc.registry, tc.profile, "text", io.Discard, io.Discard); got != tc.want {
				t.Errorf("exit %d, want %d", got, tc.want)
			}
		})
	}
}
