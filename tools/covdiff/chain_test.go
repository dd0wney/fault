package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The whole robustness chain, run over the demonstration module in testdata,
// with the three-column reading asserted.
//
// This is the selftest for the chain. covdiff_test.go proves each refusal in
// isolation, and coupling's own tests prove its measure; nothing proved that
// the four commands, run in sequence over real profiles from a real sweep,
// produce the reading the package documentation claims. The reading below
// was MEASURED 2026-09-02 on the demo module, and every figure here is that
// measurement; the comment on each fixture package says which statements
// each column holds.
//
//	                    normal      merged        robustness only
//	D1 config.Limit     1/1         1/1 100.0%    0/1   0.0%
//	D2 store.Write      8/19        19/19 100.0%  11/19 57.9%
//	D3 app.Save         5/6         6/6 100.0%    1/6   16.7%
//	                    14/26       26/26         12/26
//
// The merged column reads 100.0% for every site and exits 0. The robustness
// column says one site's error handling never executed at all and exits 1.
// That contrast is the reason the chain exists.
//
// Two shapes of assertion, and both matter. The exact figures pin the
// fixtures and the tools together, so a change in either moves a number here.
// The identity normal + robustness = merged, for every site, is what the
// subtraction MEANS: a block is robustness-only exactly when merged covered
// it and normal did not, so the two parts are disjoint and sum to the whole.
// A covdiff that copied its input, or one that dropped a block, breaks the
// identity whatever the fixture holds.
func TestTheChainReadsWhatItClaimsOverTheDemo(t *testing.T) {
	demo, err := filepath.Abs("testdata/demo")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()

	coupling := filepath.Join(work, "coupling")
	goRun(t, ".", "build", "-o", coupling, "../coupling")

	normal := filepath.Join(work, "normal.out")
	merged := filepath.Join(work, "merged.out")
	goRun(t, demo, "test", "-count=1", "-run", "TestNormalRange", "-coverpkg=./...", "-coverprofile="+normal, "./...")
	goRun(t, demo, "test", "-count=1", "-coverpkg=./...", "-coverprofile="+merged, "./...")

	robustness := filepath.Join(work, "robustness.out")
	var stdout, stderr bytes.Buffer
	if got := run(normal, merged, robustness, &stdout, &stderr); got != exitWrote {
		t.Fatalf("covdiff exit %d, want %d: %s", got, exitWrote, stderr.String())
	}

	// siteResult is the part of coupling's JSON this test reads. The
	// embedded structs flatten, so ID, Covered and Total sit at the top.
	type siteResult struct {
		ID      string
		Covered int
		Total   int
	}
	type reading struct {
		results []siteResult
		exit    int
	}
	read := func(profile string) reading {
		t.Helper()
		cmd := exec.Command(coupling, "-root", demo, "-registry", filepath.Join(demo, "couplings.tsv"), "-profile", profile, "-format", "json")
		out, err := cmd.Output()
		var exit *exec.ExitError
		code := 0
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("coupling over %s: %v", profile, err)
		}
		var results []siteResult
		if err := json.Unmarshal(out, &results); err != nil {
			t.Fatalf("coupling over %s wrote no JSON: %v\n%s", profile, err, out)
		}
		return reading{results, code}
	}
	n, m, r := read(normal), read(merged), read(robustness)

	// The exit codes carry the contrast. Merged says every site is covered.
	// Robustness-only says one site has no covered statement at all.
	if m.exit != 0 {
		t.Errorf("coupling over the merged profile exited %d, want 0: every site reads covered there", m.exit)
	}
	if r.exit != 1 {
		t.Errorf("coupling over the robustness-only profile exited %d, want 1: config.Limit has no covered statement there", r.exit)
	}

	want := map[string][3][2]int{ // id -> {normal, merged, robustness} as {covered, total}
		"D1": {{1, 1}, {1, 1}, {0, 1}},
		"D2": {{8, 19}, {19, 19}, {11, 19}},
		"D3": {{5, 6}, {6, 6}, {1, 6}},
	}
	if len(n.results) != 3 || len(m.results) != 3 || len(r.results) != 3 {
		t.Fatalf("%d/%d/%d results, want 3 in each column", len(n.results), len(m.results), len(r.results))
	}
	for i := range m.results {
		id := m.results[i].ID
		w, ok := want[id]
		if !ok {
			t.Fatalf("an unexpected site %s", id)
		}
		got := [3][2]int{
			{n.results[i].Covered, n.results[i].Total},
			{m.results[i].Covered, m.results[i].Total},
			{r.results[i].Covered, r.results[i].Total},
		}
		if got != w {
			t.Errorf("%s reads normal %d/%d, merged %d/%d, robustness %d/%d; want %d/%d, %d/%d, %d/%d",
				id, got[0][0], got[0][1], got[1][0], got[1][1], got[2][0], got[2][1],
				w[0][0], w[0][1], w[1][0], w[1][1], w[2][0], w[2][1])
		}
		// The identity, independent of the figures above.
		if n.results[i].Covered+r.results[i].Covered != m.results[i].Covered {
			t.Errorf("%s: normal %d + robustness %d != merged %d. The two parts must be disjoint and sum to the whole",
				id, n.results[i].Covered, r.results[i].Covered, m.results[i].Covered)
		}
	}

	// A control on the fixture itself: the merged column must read 100% and
	// the robustness column must not, or the demo demonstrates nothing.
	for _, res := range m.results {
		if res.Covered != res.Total {
			t.Errorf("the merged profile leaves %s at %d/%d, so the demo cannot show the false green", res.ID, res.Covered, res.Total)
		}
	}
}

// goRun runs the go command in dir and fails the test on a non-zero exit,
// with the command's own output, because a go test that did not build looks
// exactly like one that passed when only its profile is read.
func goRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %v in %s: %v\n%s", args, dir, err, out)
	}
}
