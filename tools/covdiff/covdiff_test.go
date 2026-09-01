package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// The whole subtraction, on the shape the demonstration module produced.
//
// b1 ran in both, so it is normal range and drops out. b2 ran only in the full
// profile, so it is reached only under injection and survives. b3 ran in
// neither.
func TestSubtractKeepsOnlyWhatTheBaselineMissed(t *testing.T) {
	const baseline = `mode: set
demo/store.go:10.2,11.3 1 1
demo/store.go:12.2,13.3 2 0
demo/store.go:14.2,15.3 1 0
`
	const full = `mode: set
demo/store.go:10.2,11.3 1 1
demo/store.go:12.2,13.3 2 5
demo/store.go:14.2,15.3 1 0
`
	var out strings.Builder
	if err := subtract(mustParse(t, baseline), mustParse(t, full), &out); err != nil {
		t.Fatalf("subtract: %v", err)
	}

	want := `mode: set
demo/store.go:10.2,11.3 1 0
demo/store.go:12.2,13.3 2 1
demo/store.go:14.2,15.3 1 0
`
	if out.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", out.String(), want)
	}
}

// THE REFUSAL THAT MATTERS MOST, and the reason this is a command rather than
// a shell pipeline.
//
// A baseline covering nothing makes the subtraction a no-op: the output equals
// the input and every coupling reports its whole coverage as reached only under
// injection. That reads as "every error path is exercised" — a green report
// manufactured by a control that did not run.
//
// It is not a hypothetical shape. A normal range test that failed to build, or
// a -run pattern matching no test, produces exactly this, and both print almost
// nothing on the way past.
func TestSubtractRefusesABaselineThatCoversNothing(t *testing.T) {
	const baseline = `mode: set
demo/store.go:10.2,11.3 1 0
demo/store.go:12.2,13.3 2 0
`
	const full = `mode: set
demo/store.go:10.2,11.3 1 1
demo/store.go:12.2,13.3 2 1
`
	err := subtract(mustParse(t, baseline), mustParse(t, full), io.Discard)
	if err == nil {
		t.Fatal("subtract accepted a baseline that covered nothing, which would report every " +
			"covered statement as robustness-only")
	}
	if !strings.Contains(err.Error(), "covers no block at all") {
		t.Errorf("the diagnostic does not name the cause: %v", err)
	}
}

// A baseline covering everything is a real result, not an error: the scenario
// has no statement that only a failing pass reaches. The coupling command then
// reports 0% and exits 1, which is the finding.
func TestSubtractAcceptsABaselineThatCoversEverything(t *testing.T) {
	const both = `mode: set
demo/store.go:10.2,11.3 1 1
demo/store.go:12.2,13.3 2 1
`
	var out strings.Builder
	if err := subtract(mustParse(t, both), mustParse(t, both), &out); err != nil {
		t.Fatalf("subtract: %v", err)
	}
	if strings.Contains(out.String(), " 1\n") {
		t.Errorf("a block survived a baseline that covered everything:\n%s", out.String())
	}
}

func TestSubtractRefusesDisagreeingBlockSets(t *testing.T) {
	const baseline = `mode: set
demo/store.go:10.2,11.3 1 1
`
	const full = `mode: set
demo/store.go:10.2,11.3 1 1
demo/store.go:99.2,99.9 1 1
`
	if err := subtract(mustParse(t, baseline), mustParse(t, full), io.Discard); err == nil {
		t.Error("subtract accepted profiles naming different blocks, want a refusal")
	}
}

func TestSubtractRefusesDisagreeingModes(t *testing.T) {
	const baseline = `mode: set
demo/store.go:10.2,11.3 1 1
`
	const full = `mode: count
demo/store.go:10.2,11.3 1 4
`
	if err := subtract(mustParse(t, baseline), mustParse(t, full), io.Discard); err == nil {
		t.Error("subtract accepted a set profile against a count profile, want a refusal")
	}
}

// A repeated block counts once and is covered by ANY record.
//
// -coverpkg writes one record per block per test binary — 5117 lines for 731
// distinct blocks across seven packages, measured on the fault module — so this
// is the ordinary case. Summing instead was a real defect in the coupling
// command, fixed on the same day this was written.
func TestParseCountsARepeatedBlockOnceAndCoveredByAny(t *testing.T) {
	const text = `mode: set
demo/store.go:10.2,11.3 1 0
demo/store.go:12.2,13.3 2 0
demo/store.go:10.2,11.3 1 1
demo/store.go:12.2,13.3 2 0
demo/store.go:10.2,11.3 1 0
demo/store.go:12.2,13.3 2 0
`
	p := mustParse(t, text)
	if len(p.order) != 2 {
		t.Fatalf("%d distinct blocks, want 2", len(p.order))
	}
	if !p.covered["demo/store.go:10.2,11.3"] {
		t.Error("a block covered by the middle record only is reported uncovered; a zero " +
			"either side of it must not erase the one that ran")
	}
	if p.covered["demo/store.go:12.2,13.3"] {
		t.Error("a block no record covered is reported covered")
	}
}

func TestParseRefuses(t *testing.T) {
	for name, text := range map[string]string{
		"no mode header":        "demo/store.go:10.2,11.3 1 1\n",
		"empty":                 "",
		"mode only, no blocks":  "mode: set\n",
		"truncated line":        "mode: set\ndemo/store.go:10.2,11.3 1\n",
		"non-numeric count":     "mode: set\ndemo/store.go:10.2,11.3 1 x\n",
		"non-numeric statement": "mode: set\ndemo/store.go:10.2,11.3 x 1\n",
		"statements disagree":   "mode: set\ndemo/store.go:10.2,11.3 1 1\ndemo/store.go:10.2,11.3 2 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(strings.NewReader(text)); err == nil {
				t.Errorf("parse accepted %q, want a refusal", text)
			}
		})
	}
}

// The command's own exit codes, read directly rather than through a pipeline.
func TestExitCodes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) string {
		t.Helper()
		p := dir + "/" + name
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	baseline := write("normal.out", "mode: set\ndemo/store.go:10.2,11.3 1 1\ndemo/store.go:12.2,13.3 2 0\n")
	full := write("merged.out", "mode: set\ndemo/store.go:10.2,11.3 1 1\ndemo/store.go:12.2,13.3 2 1\n")
	empty := write("empty.out", "mode: set\ndemo/store.go:10.2,11.3 1 0\ndemo/store.go:12.2,13.3 2 0\n")

	cases := []struct {
		name                   string
		baseline, profile, out string
		want                   int
	}{
		{"a profile is written", baseline, full, dir + "/robustness.out", exitWrote},
		{"no flags", "", "", "", exitCannotTell},
		{"the baseline does not exist", dir + "/nope.out", full, "", exitCannotTell},
		{"the profile does not exist", baseline, dir + "/nope.out", "", exitCannotTell},
		{"the baseline covered nothing", empty, full, "", exitCannotTell},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// stdout and stderr are read, not discarded: TestExitCodes in the
			// coupling command sends both to io.Discard, and the mutation
			// baseline records that this leaves 47 mutants alive because no
			// test reads a single printed word.
			var stdout, stderr strings.Builder
			if got := run(tc.baseline, tc.profile, tc.out, &stdout, &stderr); got != tc.want {
				t.Errorf("exit %d, want %d (stderr: %s)", got, tc.want, stderr.String())
			}
			if tc.want == exitCannotTell && stderr.Len() == 0 {
				t.Error("a refusal printed nothing to stderr, so a caller is told only a number")
			}
			if tc.want == exitCannotTell && !strings.HasPrefix(stderr.String(), "covdiff: ") {
				t.Errorf("a diagnostic that does not name the command: %q", stderr.String())
			}
		})
	}
}

func mustParse(t *testing.T, text string) *profile {
	t.Helper()
	p, err := parse(strings.NewReader(text))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

// -o writes the file, and the file holds the subtraction.
//
// TestExitCodes passed an -o path and never opened it, which is the shape this
// repository records as "a string replacement without an assert": the code ran,
// the exit code was right, and nothing looked at what came out. The mutation
// baseline for the coupling command records 47 survivors from exactly this.
func TestTheOutputFileHoldsTheSubtraction(t *testing.T) {
	dir := t.TempDir()
	baseline := dir + "/normal.out"
	full := dir + "/merged.out"
	out := dir + "/robustness.out"
	mustWrite(t, baseline, "mode: set\ndemo/a.go:1.1,2.2 1 1\ndemo/a.go:3.3,4.4 2 0\n")
	mustWrite(t, full, "mode: set\ndemo/a.go:1.1,2.2 1 1\ndemo/a.go:3.3,4.4 2 7\n")

	var stdout, stderr strings.Builder
	if got := run(baseline, full, out, &stdout, &stderr); got != exitWrote {
		t.Fatalf("exit %d, want %d (%s)", got, exitWrote, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("with -o the profile must not also go to stdout, got %q", stdout.String())
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the command exited 0 and wrote no file: %v", err)
	}
	const want = "mode: set\ndemo/a.go:1.1,2.2 1 0\ndemo/a.go:3.3,4.4 2 1\n"
	if string(got) != want {
		t.Errorf("the file holds:\n%s\nwant:\n%s", got, want)
	}
}

// Without -o the profile goes to stdout, and it is the same bytes.
func TestWithoutAnOutputPathTheProfileGoesToStdout(t *testing.T) {
	dir := t.TempDir()
	baseline := dir + "/normal.out"
	full := dir + "/merged.out"
	mustWrite(t, baseline, "mode: set\ndemo/a.go:1.1,2.2 1 1\ndemo/a.go:3.3,4.4 2 0\n")
	mustWrite(t, full, "mode: set\ndemo/a.go:1.1,2.2 1 1\ndemo/a.go:3.3,4.4 2 7\n")

	var stdout, stderr strings.Builder
	if got := run(baseline, full, "", &stdout, &stderr); got != exitWrote {
		t.Fatalf("exit %d, want %d (%s)", got, exitWrote, stderr.String())
	}
	const want = "mode: set\ndemo/a.go:1.1,2.2 1 0\ndemo/a.go:3.3,4.4 2 1\n"
	if stdout.String() != want {
		t.Errorf("stdout holds:\n%s\nwant:\n%s", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Errorf("a successful run wrote to stderr: %q", stderr.String())
	}
}

// Each refusal names which of the two profiles it could not use.
//
// "covdiff: open /x/y: no such file" tells a caller a file is missing and not
// WHICH ROLE it was playing, and the two flags are easy to transpose.
func TestARefusalNamesWhichProfileItIs(t *testing.T) {
	dir := t.TempDir()
	good := dir + "/good.out"
	mustWrite(t, good, "mode: set\ndemo/a.go:1.1,2.2 1 1\n")
	missing := dir + "/nope.out"

	for _, tc := range []struct {
		name              string
		baseline, profile string
		want              string
	}{
		{"the baseline", missing, good, "the baseline"},
		{"the profile", good, missing, "the profile"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			if got := run(tc.baseline, tc.profile, "", &stdout, &stderr); got != exitCannotTell {
				t.Fatalf("exit %d, want %d", got, exitCannotTell)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("the diagnostic does not say which profile failed: %q", stderr.String())
			}
		})
	}
}

// Missing flags print the usage line, because a bare "required" tells a caller
// nothing they can act on.
func TestMissingFlagsPrintTheInvocation(t *testing.T) {
	var stdout, stderr strings.Builder
	if got := run("", "", "", &stdout, &stderr); got != exitCannotTell {
		t.Fatalf("exit %d, want %d", got, exitCannotTell)
	}
	for _, want := range []string{"-baseline", "-profile", "go tool covdiff"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("the diagnostic omits %q: %q", want, stderr.String())
		}
	}
}

// An output path that cannot be created is a refusal, not a silent success.
func TestAnUnwritableOutputPathRefuses(t *testing.T) {
	dir := t.TempDir()
	baseline := dir + "/normal.out"
	full := dir + "/merged.out"
	mustWrite(t, baseline, "mode: set\ndemo/a.go:1.1,2.2 1 1\n")
	mustWrite(t, full, "mode: set\ndemo/a.go:1.1,2.2 1 1\n")

	var stdout, stderr strings.Builder
	got := run(baseline, full, dir+"/no-such-directory/out.out", &stdout, &stderr)
	if got != exitCannotTell {
		t.Errorf("exit %d, want %d for an output path that cannot be created", got, exitCannotTell)
	}
}

func mustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
