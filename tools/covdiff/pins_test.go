package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The pins in this file each kill a survivor the ./covdiff/ baseline row
// recorded as unread. Every survivor was read on 2026-09-02: 29 were real
// gaps and 6 equivalent, and the equivalents are named in the baseline row.
// The tests are grouped as the survivors were, so one fixture kills several.

// An empty line between two blocks is skipped, and the blocks after it are
// read. The mutant that turned the skip into a break dropped every block
// after the first empty line, and a profile is full of them at package
// boundaries.
func TestParseSkipsAnEmptyLineAndReadsPastIt(t *testing.T) {
	p := mustParse(t, "mode: set\na.go:1.1,2.1 1 1\n\nb.go:1.1,2.1 1 0\n")
	want := []string{"a.go:1.1,2.1", "b.go:1.1,2.1"}
	if !slices.Equal(p.order, want) {
		t.Fatalf("order = %q, want %q: the block after the empty line was dropped", p.order, want)
	}
}

// A block before the mode header is a refusal, even when a header follows.
func TestParseRefusesABlockBeforeTheHeaderEvenWhenOneFollows(t *testing.T) {
	_, err := parse(strings.NewReader("a.go:1.1,2.1 1 1\nmode: set\nb.go:1.1,2.1 1 0\n"))
	if err == nil || !strings.Contains(err.Error(), "before the") {
		t.Fatalf("parse = %v, want the block-before-the-header refusal", err)
	}
}

// A reader that fails is a refusal that carries the reader's own error.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestParseCarriesAReadError(t *testing.T) {
	boom := errors.New("boom")
	_, err := parse(failingReader{boom})
	if !errors.Is(err, boom) {
		t.Fatalf("parse returned %v, want the reader's own error", err)
	}
}

// Empty input names the missing header, and nothing else.
func TestParseNamesTheMissingHeaderForEmptyInput(t *testing.T) {
	_, err := parse(strings.NewReader(""))
	if err == nil || err.Error() != `no "mode:" header, so this is not a coverage profile` {
		t.Fatalf("parse returned %v, want the missing-header refusal", err)
	}
}

// A block only the BASELINE names is a disagreement, as one only the profile
// names is. The second loop in subtract is what finds it, and both mutants
// that emptied that loop survived because no test gave the baseline a block
// the profile lacked.
func TestSubtractRefusesABlockOnlyTheBaselineNames(t *testing.T) {
	baseline := mustParse(t, "mode: set\na.go:1.1,2.1 1 1\nb.go:1.1,2.1 1 1\n")
	prof := mustParse(t, "mode: set\na.go:1.1,2.1 1 1\n")
	if err := subtract(baseline, prof, &bytes.Buffer{}); err == nil {
		t.Fatal("subtract accepted a baseline that names a block the profile lacks")
	}
}

// Four disagreeing blocks: the message counts four, names the first three in
// sorted order, and stops there. Five survivors sat on that cap and the sort.
func TestSubtractNamesThreeOfManyDisagreeingBlocksInOrder(t *testing.T) {
	baseline := mustParse(t, "mode: set\nz.go:1.1,2.1 1 1\n")
	prof := mustParse(t, "mode: set\nz.go:1.1,2.1 1 1\nd.go:1.1,2.1 1 1\nb.go:1.1,2.1 1 1\na.go:1.1,2.1 1 1\nc.go:1.1,2.1 1 1\n")
	err := subtract(baseline, prof, &bytes.Buffer{})
	if err == nil {
		t.Fatal("subtract accepted four blocks the baseline lacks")
	}
	msg := err.Error()
	if !strings.Contains(msg, "4 block(s)") {
		t.Errorf("the refusal does not count four: %v", err)
	}
	a, b, c := strings.Index(msg, "a.go"), strings.Index(msg, "b.go"), strings.Index(msg, "c.go")
	if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
		t.Errorf("the refusal must name a.go, b.go and c.go in that order: %v", err)
	}
	if strings.Contains(msg, "d.go") {
		t.Errorf("the refusal names a fourth block past the cap of three: %v", err)
	}
}

// A parse error names its line, counting from 1, and names the field that
// was bad and not its neighbour. The line counter's start and the field
// index in two messages each survived, because nothing read the text.
func TestParseErrorsNameTheLineAndTheBadField(t *testing.T) {
	for _, tc := range []struct{ name, text, wants, refuses string }{
		// The label and the quoted value together, because the wrapped Atoi
		// error repeats the bad value on its own, so the value alone is in
		// the message whichever field the label names. A first version of
		// this table asserted the value alone and let that mutant live.
		{"execution count on line 2", "mode: set\nx.go:1.1,2.1 3 BAD\n", "line 2", ""},
		{"the statement count field", "mode: set\nx.go:1.1,2.1 NOTNUM 7\n", `statement count "NOTNUM"`, `statement count "7"`},
		{"the execution count field", "mode: set\nx.go:1.1,2.1 3 BAD\n", `execution count "BAD"`, `execution count "3"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(strings.NewReader(tc.text))
			if err == nil {
				t.Fatal("parse accepted the malformed line")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("the refusal lacks %s: %v", tc.wants, err)
			}
			if tc.refuses != "" && strings.Contains(err.Error(), tc.refuses) {
				t.Errorf("the refusal names the wrong field %s: %v", tc.refuses, err)
			}
		})
	}
}

// One flag missing is refused exactly as both missing are, and the exit codes
// are the documented numbers, 0 and 2, compared as numbers. TestExitCodes
// compares against the constants by name, so a constant that changed value
// moved both sides of that comparison together.
func TestRunRefusesOneMissingFlagAndExitsWithTheDocumentedNumbers(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "normal.out")
	prof := filepath.Join(dir, "merged.out")
	mustWrite(t, baseline, "mode: set\na.go:1.1,2.1 1 1\n")
	mustWrite(t, prof, "mode: set\na.go:1.1,2.1 1 1\n")

	for _, tc := range []struct{ name, b, p string }{
		{"no baseline", "", prof},
		{"no profile", baseline, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tc.b, tc.p, "", &stdout, &stderr); got != 2 {
				t.Errorf("exit %d, want the documented 2", got)
			}
			if !strings.Contains(stderr.String(), "both required") {
				t.Errorf("stderr does not say both flags are required: %q", stderr.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	if got := run(baseline, prof, "", &stdout, &stderr); got != 0 {
		t.Errorf("exit %d, want the documented 0: %s", got, stderr.String())
	}
}

// The refusals for a bad output path and a missing input file each name the
// path on stderr. Both messages survived, because nothing read stderr there.
func TestRunNamesTheBadPathOnStderr(t *testing.T) {
	dir := t.TempDir()
	baseline := filepath.Join(dir, "normal.out")
	prof := filepath.Join(dir, "merged.out")
	mustWrite(t, baseline, "mode: set\na.go:1.1,2.1 1 1\n")
	mustWrite(t, prof, "mode: set\na.go:1.1,2.1 1 1\n")

	var stdout, stderr bytes.Buffer
	missing := filepath.Join(dir, "no-such-file.out")
	if got := run(missing, prof, "", &stdout, &stderr); got != exitCannotTell {
		t.Errorf("a missing baseline: exit %d, want %d", got, exitCannotTell)
	}
	if !strings.Contains(stderr.String(), "no-such-file.out") {
		t.Errorf("the refusal does not name the missing file: %q", stderr.String())
	}

	stderr.Reset()
	bad := filepath.Join(dir, "no-such-directory", "out.out")
	if got := run(baseline, prof, bad, &stdout, &stderr); got != exitCannotTell {
		t.Errorf("an unwritable output path: exit %d, want %d", got, exitCannotTell)
	}
	if !strings.Contains(stderr.String(), "no-such-directory") {
		t.Errorf("the refusal does not name the bad output path: %q", stderr.String())
	}
}

// main wires the flags to run and run's code to the process exit, and no
// test ran the process. This builds the command and runs it twice: once with
// every flag, reading the output file and exit 0, and once with none,
// reading exit 2.
func TestTheCommandWiresFlagsToRunAndRunToTheExitCode(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "covdiff")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	baseline := filepath.Join(dir, "normal.out")
	prof := filepath.Join(dir, "merged.out")
	outPath := filepath.Join(dir, "robustness.out")
	mustWrite(t, baseline, "mode: set\na.go:1.1,2.1 1 1\nb.go:1.1,2.1 1 0\n")
	mustWrite(t, prof, "mode: set\na.go:1.1,2.1 1 1\nb.go:1.1,2.1 1 1\n")

	if out, err := exec.Command(bin, "-baseline", baseline, "-profile", prof, "-o", outPath).CombinedOutput(); err != nil {
		t.Fatalf("the command failed with every flag set: %v\n%s", err, out)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "mode: set\na.go:1.1,2.1 1 0\nb.go:1.1,2.1 1 1\n"; string(got) != want {
		t.Errorf("the output file holds:\n%s\nwant:\n%s", got, want)
	}

	err = exec.Command(bin).Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 {
		t.Errorf("the command with no flags gave %v, want exit 2", err)
	}
}
