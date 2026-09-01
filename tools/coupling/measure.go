package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// measure attributes a Go coverage profile to the resolved sites.
//
// A profile line looks like this, after the "mode:" header:
//
//	github.com/dd0wney/fault/fs/fault.go:57.75,58.18 1 4
//	 path                        :start.col,end.col  stmts count
//
// A block belongs to a site when the file matches EXACTLY and the block's start
// line falls inside the site's range. The bash original used a substring test
// and matched pkg/storage2 for a registry naming pkg/storage.
//
// A BLOCK THAT APPEARS MORE THAN ONCE COUNTS ONCE, and it is covered when any
// record for it has a non-zero count. That is the rule Go's own cover tooling
// uses, and it is not an edge case here.
//
// MEASURED 2026-09-01 on the fault repository. `go test -coverpkg=./...` over
// seven packages writes 5117 profile lines for 731 DISTINCT blocks, because
// every test binary emits a record for every instrumented block whether it ran
// it or not. Summing them grew the numerator and the denominator sevenfold and
// moved the reported figure from 98.6% to 20.7%, with nothing about the code or
// the tests changed.
//
// -coverpkg is not exotic. It is what a caller NEEDS when a coupling site sits
// in a package whose statements are executed only from another package's tests:
// without it that site reports 0/N and this tool exits 1, and with it the tool
// used to report a number that meant nothing. Both readings are wrong, in
// opposite directions.
//
// Four refusals, and they are the valuable part of the tool:
//
//  1. A profile with no "mode:" header, or a line this cannot parse, is a
//     refusal. A check that read nothing must not report a pass.
//
//  2. A site that matched no statement at all is a refusal, not "n/a". Every
//     real function has statements, so zero means the profile does not cover
//     that package or resolve found the wrong file. Reporting it beside real
//     percentages is how a half-written profile gets read as a finished one.
//
//  3. Two records for one block that disagree about its statement count is a
//     refusal. Go emits the same count every time, so a disagreement means the
//     profile was assembled from builds of different source, and any number
//     drawn from it describes no tree that ever existed.
//
//  4. A site with statements but none covered is exit 1, and that is a property
//     of the results rather than an error here. report decides it.
func measure(profile io.Reader, sites []Site) ([]Result, error) {
	results := make([]Result, len(sites))
	for i, s := range sites {
		results[i] = Result{Site: s}
	}

	// Collect distinct blocks first, then attribute. Attributing as each line
	// arrives is what counted a repeated block once per record.
	type block struct {
		file    string
		start   int
		stmts   int
		covered bool
	}
	var order []string
	blocks := make(map[string]*block)

	sawMode := false
	sc := bufio.NewScanner(profile)
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "mode:") {
			sawMode = true
			continue
		}
		if !sawMode {
			return nil, fmt.Errorf("line %d: a coverage block before the \"mode:\" header: %q", line, text)
		}

		key, file, start, stmts, count, err := parseBlock(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}

		b, seen := blocks[key]
		if !seen {
			b = &block{file: file, start: start, stmts: stmts}
			blocks[key] = b
			order = append(order, key)
		} else if b.stmts != stmts {
			return nil, fmt.Errorf("line %d: block %s has %d statements here and %d earlier, "+
				"so this profile was assembled from builds of different source",
				line, key, stmts, b.stmts)
		}
		// Covered by ANY record. Package A's test binary reports 0 for a block
		// that package B's binary reports 1 for, and the block did run.
		if count > 0 {
			b.covered = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawMode {
		return nil, fmt.Errorf("no \"mode:\" header, so this is not a coverage profile")
	}

	// Attribute in the order the profile first named each block, so the result
	// does not depend on map iteration order.
	for _, key := range order {
		b := blocks[key]
		for i := range results {
			s := &results[i]
			if s.File != b.file || b.start < s.Start || b.start > s.End {
				continue
			}
			s.Total += b.stmts
			if b.covered {
				s.Covered += b.stmts
			}
		}
	}

	// A site with no statements is not a zero-percent result. It means nothing
	// matched, and a number produced from nothing must not be reported.
	var unmatched []string
	for _, r := range results {
		if r.Total == 0 {
			unmatched = append(unmatched, fmt.Sprintf("%s (%s.%s in %s)", r.ID, r.Package, r.Symbol, r.File))
		}
	}
	if len(unmatched) > 0 {
		return nil, fmt.Errorf("these coupling sites matched no statement in the profile, so "+
			"the profile does not cover them or they resolved to the wrong file: %s",
			strings.Join(unmatched, ", "))
	}
	return results, nil
}

// parseBlock reads one coverage block: path:startLine.col,endLine.col stmts count.
//
// The key it returns is the block's exact span, which is what identifies it
// across records. Two different blocks can share a start line, so the start
// line alone is not an identity.
func parseBlock(text string) (key, file string, start, stmts, count int, err error) {
	f := strings.Fields(text)
	if len(f) != 3 {
		return "", "", 0, 0, 0, fmt.Errorf("%d fields, want 3 (block, statements, count): %q", len(f), text)
	}
	key = f[0]

	colon := strings.LastIndex(f[0], ":")
	if colon < 0 {
		return "", "", 0, 0, 0, fmt.Errorf("no \":\" separating the file from the block: %q", f[0])
	}
	file = f[0][:colon]

	from, _, ok := strings.Cut(f[0][colon+1:], ",")
	if !ok {
		return "", "", 0, 0, 0, fmt.Errorf("no \",\" separating the block's start from its end: %q", f[0])
	}
	lineText, _, ok := strings.Cut(from, ".")
	if !ok {
		return "", "", 0, 0, 0, fmt.Errorf("no \".\" separating the line from the column: %q", from)
	}
	if start, err = strconv.Atoi(lineText); err != nil {
		return "", "", 0, 0, 0, fmt.Errorf("start line %q: %w", lineText, err)
	}
	if stmts, err = strconv.Atoi(f[1]); err != nil {
		return "", "", 0, 0, 0, fmt.Errorf("statement count %q: %w", f[1], err)
	}
	if count, err = strconv.Atoi(f[2]); err != nil {
		return "", "", 0, 0, 0, fmt.Errorf("execution count %q: %w", f[2], err)
	}
	return key, file, start, stmts, count, nil
}
