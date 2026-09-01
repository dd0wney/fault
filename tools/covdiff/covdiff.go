// Command covdiff subtracts one Go coverage profile from another, so that
// `go tool coupling` can measure the statements only a FAILING pass reached.
//
// # What it is for
//
// DO-178C 6.4.2.2 asks for two kinds of test case. Normal range checks the
// response to expected inputs. Robustness tests behaviour "when abnormal
// conditions and faults are present". Objective 6.4.4.d separately asks for
// coverage of the data- and control-coupling interfaces between components.
//
// A sweep produces both categories in one loop. github.com/dd0wney/fault's
// Sweep yields K+1 passes for a K-operation scenario: one that fails each
// operation in turn, plus a terminating pass that fails none. The terminating
// pass IS the normal range case.
//
// Nothing separates them afterwards. `go test -coverprofile` accumulates
// counters across the binary, so the profile a coupling measure reads is the
// union. A coupling site touched only by the normal range case therefore
// reports as covered, and its error path has never executed.
//
// MEASURED 2026-09-01 on a two-package demonstration module. Two declared
// couplings, both reading 100.0% in the merged profile, exit 0:
//
//	                        normal only    merged    robustness only
//	store.Write             6/12  50.0%   12/12 100%   6/12  50.0%
//	config.Limit            1/1  100.0%    1/1  100%   0/1    0.0%
//
// The two are indistinguishable in the merged column. One has had its error
// handling exercised exhaustively and the other has none exercised at all.
// That is the objective's letter satisfied while the thing it exists to find
// goes unreported.
//
// # How
//
// Run the scenario twice and subtract:
//
//	go test -run TestNormalRange -coverpkg=./... -coverprofile=normal.out ./...
//	go test              -coverpkg=./... -coverprofile=merged.out ./...
//	go tool covdiff -baseline normal.out -profile merged.out -o robustness.out
//	go tool coupling -root . -registry docs/couplings.tsv -profile robustness.out
//
// A block in the output is covered when `profile` covered it and `baseline`
// did not. Statement counts and block spans are carried through unchanged, so
// the result is an ordinary profile that any tool reading the format accepts.
//
// The normal range run needs no new machinery: a zero fault.Points arms
// nothing, so every operation succeeds. Writing that test is not an extra cost
// imposed by this tool — it is the other half of 6.4.2.2, which the standard
// asks for anyway.
//
// # Why there is no per-pass capture here
//
// An earlier design captured one coverage counter set per sweep pass, through
// runtime/coverage.ClearCounters and WriteCountersDir. It was dropped after
// this was measured working. Per-pass capture would answer a finer question —
// WHICH operation's failure reached an interface — that the objective does not
// ask. It costs atomic counter mode, a CI step that cannot also produce the
// ordinary profile, and a runtime dependency in a package that has none.
//
// # Exit codes
//
// Read this command's status, never a pipeline's.
//
//	0  a profile was written
//	2  the subtraction could not be trusted
package main

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// profile is one parsed coverage profile.
type profile struct {
	mode    string
	order   []string       // block spans, in the order first seen
	stmts   map[string]int // span -> statement count
	covered map[string]bool
}

// parse reads a Go coverage profile.
//
// A block that appears more than once counts once, and it is covered when ANY
// record for it has a non-zero count. `go test -coverpkg=./...` writes one
// record per block PER TEST BINARY — measured at 5117 lines for 731 distinct
// blocks across seven packages — so this is the ordinary case rather than an
// edge one. The same rule is in the coupling command's measure.go, and the two
// must not drift.
func parse(r io.Reader) (*profile, error) {
	p := &profile{stmts: map[string]int{}, covered: map[string]bool{}}

	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "mode:") {
			p.mode = text
			continue
		}
		if p.mode == "" {
			return nil, fmt.Errorf("line %d: a coverage block before the \"mode:\" header: %q", line, text)
		}

		fields := strings.Fields(text)
		if len(fields) != 3 {
			return nil, fmt.Errorf("line %d: %d fields, want 3 (block, statements, count): %q",
				line, len(fields), text)
		}
		span := fields[0]
		stmts, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("line %d: statement count %q: %w", line, fields[1], err)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("line %d: execution count %q: %w", line, fields[2], err)
		}

		if was, seen := p.stmts[span]; seen {
			if was != stmts {
				return nil, fmt.Errorf("line %d: block %s has %d statements here and %d earlier, "+
					"so this profile was assembled from builds of different source", line, span, stmts, was)
			}
		} else {
			p.stmts[span] = stmts
			p.order = append(p.order, span)
		}
		if count > 0 {
			p.covered[span] = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if p.mode == "" {
		return nil, fmt.Errorf("no \"mode:\" header, so this is not a coverage profile")
	}
	if len(p.order) == 0 {
		return nil, fmt.Errorf("no coverage blocks, so there is nothing to subtract")
	}
	return p, nil
}

// subtract writes the blocks that profile covered and baseline did not.
//
// Three refusals, and the first is the reason this command exists as a command
// rather than as a shell pipeline.
//
//  1. A BASELINE THAT COVERED NOTHING. The subtraction is then a no-op, the
//     output equals the input, and every coupling reports its full coverage as
//     robustness-only. That reads as "every error path is exercised" and is the
//     most dangerous result this tool could produce: it is a green report
//     manufactured by a control that did not run. A normal range test that
//     failed to build, or a -run pattern that matched nothing, produces exactly
//     this. Both print almost nothing and look like success.
//
//  2. BLOCK SETS THAT DISAGREE. If one profile names a block the other does
//     not, the two came from different source and no statement about their
//     difference means anything.
//
//  3. MODES THAT DISAGREE. A "set" profile records whether a block ran and a
//     "count" profile records how often. Subtracting across the two compares
//     values that are not the same kind of thing.
func subtract(baseline, prof *profile, w io.Writer) error {
	if len(baseline.covered) == 0 {
		return fmt.Errorf("the baseline covers no block at all, so subtracting it changes nothing " +
			"and every coupling would report its whole coverage as reached only under injection. " +
			"A normal range run that did not build, or a -run pattern that matched no test, " +
			"produces exactly this")
	}
	if baseline.mode != prof.mode {
		return fmt.Errorf("the baseline is %q and the profile is %q, and the two do not measure "+
			"the same thing", baseline.mode, prof.mode)
	}

	var onlyInOne []string
	for span := range prof.stmts {
		if _, ok := baseline.stmts[span]; !ok {
			onlyInOne = append(onlyInOne, span)
		}
	}
	for span := range baseline.stmts {
		if _, ok := prof.stmts[span]; !ok {
			onlyInOne = append(onlyInOne, span)
		}
	}
	if len(onlyInOne) > 0 {
		sort.Strings(onlyInOne)
		shown := onlyInOne
		if len(shown) > 3 {
			shown = shown[:3]
		}
		return fmt.Errorf("%d block(s) appear in one profile and not the other, so they were "+
			"built from different source: %s", len(onlyInOne), strings.Join(shown, ", "))
	}

	bw := bufio.NewWriter(w)
	if _, err := fmt.Fprintln(bw, prof.mode); err != nil {
		return err
	}
	for _, span := range prof.order {
		count := 0
		if prof.covered[span] && !baseline.covered[span] {
			count = 1
		}
		if _, err := fmt.Fprintf(bw, "%s %d %d\n", span, prof.stmts[span], count); err != nil {
			return err
		}
	}
	return bw.Flush()
}
