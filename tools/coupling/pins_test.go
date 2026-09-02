package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The pins in this file each kill a mutant the ./coupling/ baseline row
// recorded as a survivor. The tool produces the number a caller reads, so a
// survivor here is a way for that number to be wrong with every gate green.

// A block counts when its start line is inside the site, inclusive at BOTH
// ends, and never when it is one line outside or in another file.
//
// The predicate is `s.File != b.file || b.start < s.Start || b.start > s.End`,
// and the mutants that survived turn either comparison inclusive the other
// way, which drops the block that starts on the site's first or last line.
// The fixture profile never had a block on a site boundary, so nothing saw it.
func TestMeasureCountsABlockOnEachBoundaryAndNoneBeyond(t *testing.T) {
	sites := []Site{{Coupling: Coupling{ID: "X"}, File: "x.go", Start: 10, End: 20}}
	profile := strings.NewReader(strings.Join([]string{
		"mode: set",
		"x.go:9.1,9.5 1 1",   // one line before the site: no
		"x.go:10.1,10.5 1 1", // the first line: yes
		"x.go:20.1,20.5 1 1", // the last line: yes
		"x.go:21.1,21.5 1 1", // one line after: no
		"y.go:15.1,15.5 1 1", // inside the range, another file: no
		"",
	}, "\n"))

	got, err := measure(profile, sites)
	if err != nil {
		t.Fatalf("measure: %v", err)
	}
	if got[0].Total != 2 || got[0].Covered != 2 {
		t.Errorf("X counted %d/%d statements, want 2/2: the blocks on lines 10 and 20 and no other", got[0].Covered, got[0].Total)
	}
}

// One site of several matching nothing is a refusal, exactly as all of them
// matching nothing is. The mutant that survived let a single unmatched site
// through, and the baseline row recorded it: "a profile can go missing exactly
// one of several sites without refusal". The fixture had two sites and a
// profile that missed both.
func TestMeasureRefusesWhenOneOfSeveralSitesMatchesNothing(t *testing.T) {
	sites := []Site{
		{Coupling: Coupling{ID: "X", Package: "p", Symbol: "X"}, File: "x.go", Start: 10, End: 20},
		{Coupling: Coupling{ID: "Y", Package: "p", Symbol: "Y"}, File: "y.go", Start: 10, End: 20},
	}
	profile := strings.NewReader("mode: set\nx.go:10.1,10.5 1 1\n")

	_, err := measure(profile, sites)
	if err == nil {
		t.Fatal("measure accepted a profile that matched one site of two, want a refusal")
	}
	if !strings.Contains(err.Error(), "Y") || strings.Contains(err.Error(), "X (") {
		t.Errorf("the refusal must name the unmatched site Y and not the matched site X: %v", err)
	}
}

// The printed figure and the JSON are the tool's whole output, and nothing
// read either: TestExitCodes sends both streams to io.Discard, so 47 mutants in
// main.go that change a percentage, a row or a field survived. This reads the
// text and decodes the JSON for the fixture whose numbers are worked by hand
// from BOTH fixtures, the profile and the source it names.
//
// Load resolves to lines 8-16 and the profile puts four blocks inside, so
// Load is 4. Save resolves to lines 19-24 and the profile puts three blocks
// at 18, 19 and 21; the first sits on the doc-comment line, outside the
// resolved range, so Save is 2 and not the 3 a reading of the profile alone
// gives. TestMeasureCountsARepeatedBlockOnce records the same trap. The
// mixed profile covers Load only: 4/6 = 66.7%, one site uncovered.
func TestTheReportPrintsTheFigureAndTheJSONCarriesIt(t *testing.T) {
	var out, errs bytes.Buffer
	if got := run("testdata", "testdata/registry.tsv", "testdata/profile-mixed.out", "text", &out, &errs); got != exitUncovered {
		t.Fatalf("exit %d, want %d", got, exitUncovered)
	}
	if errs.Len() != 0 {
		t.Errorf("stderr is not empty on a successful run: %q", errs.String())
	}
	text := out.String()
	for _, want := range []string{
		"4/6 statements across 2 coupling sites = 66.7%",
		"1 coupling site(s) have no covered statement at all.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the text report lacks %q:\n%s", want, text)
		}
	}
	rows := map[string][]string{
		"C1": {"4/4", "100.0%", "control", "fixture.Load"},
		"C2": {"0/2", "0.0%", "data", "fixture.Save"},
	}
	for id, wants := range rows {
		line := lineNaming(text, id)
		if line == "" {
			t.Errorf("the text report has no row for %s:\n%s", id, text)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(line, want) {
				t.Errorf("the %s row lacks %q: %q", id, want, line)
			}
		}
	}

	out.Reset()
	errs.Reset()
	if got := run("testdata", "testdata/registry.tsv", "testdata/profile-mixed.out", "json", &out, &errs); got != exitUncovered {
		t.Fatalf("json: exit %d, want %d", got, exitUncovered)
	}
	var results []Result
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("the JSON output does not decode: %v\n%s", err, out.String())
	}
	if len(results) != 2 {
		t.Fatalf("%d JSON results, want 2", len(results))
	}
	if results[0].ID != "C1" || results[0].Covered != 4 || results[0].Total != 4 {
		t.Errorf("JSON C1 = %s %d/%d, want C1 4/4", results[0].ID, results[0].Covered, results[0].Total)
	}
	if results[1].ID != "C2" || results[1].Covered != 0 || results[1].Total != 2 {
		t.Errorf("JSON C2 = %s %d/%d, want C2 0/2", results[1].ID, results[1].Covered, results[1].Total)
	}

	// The all-covered fixture, so the exit-0 shape of the summary is pinned too.
	out.Reset()
	if got := run("testdata", "testdata/registry.tsv", "testdata/profile-all-covered.out", "text", &out, &errs); got != exitCovered {
		t.Fatalf("all covered: exit %d, want %d", got, exitCovered)
	}
	if !strings.Contains(out.String(), "6/6 statements across 2 coupling sites = 100.0%") {
		t.Errorf("the all-covered report lacks the 100.0%% summary:\n%s", out.String())
	}
	if strings.Contains(out.String(), "no covered statement") {
		t.Errorf("the all-covered report claims an uncovered site:\n%s", out.String())
	}
}

// lineNaming returns the first line of text that carries id as a word.
func lineNaming(text, id string) string {
	for _, line := range strings.Split(text, "\n") {
		for _, field := range strings.Fields(line) {
			if field == id {
				return line
			}
		}
	}
	return ""
}

// Every malformed block line is a refusal, named, and never a panic. The
// baseline row recorded that the missing-colon case "would panic on a
// negative slice" once the check is gone, and that nothing drove any of the
// seven parse errors, so all seven survived as mutants.
func TestParseBlockRefusesEachMalformedShape(t *testing.T) {
	for _, tc := range []struct{ name, line, names string }{
		{"two fields", "x.go:10.1,11.1 1", "fields"},
		{"four fields", "x.go:10.1,11.1 1 1 1", "fields"},
		{"no colon", "x.go 1 1", "\":\""},
		{"no comma", "x.go:10.1 1 1", "\",\""},
		{"no dot", "x.go:10,11.1 1 1", "\".\""},
		{"start line not a number", "x.go:a.1,11.1 1 1", "start line"},
		{"statement count not a number", "x.go:10.1,11.1 a 1", "statement count"},
		{"execution count not a number", "x.go:10.1,11.1 1 a", "execution count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, _, err := parseBlock(tc.line)
			if err == nil {
				t.Fatalf("parseBlock accepted %q", tc.line)
			}
			if !strings.Contains(err.Error(), tc.names) {
				t.Errorf("the refusal for %q does not name the problem %q: %v", tc.line, tc.names, err)
			}
		})
	}
}

// And measure carries a malformed line up as a refusal with its line number,
// rather than skipping it. A profile with one bad line is not a profile.
func TestMeasureRefusesAMalformedLineByNumber(t *testing.T) {
	sites := []Site{{Coupling: Coupling{ID: "X"}, File: "x.go", Start: 10, End: 20}}
	profile := strings.NewReader("mode: set\nx.go:10.1,10.5 1 1\nx.go:11.1 1 1\n")

	_, err := measure(profile, sites)
	if err == nil {
		t.Fatal("measure accepted a profile with a malformed third line")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("the refusal does not name line 3: %v", err)
	}
}
