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
// Three refusals, and they are the valuable part of the tool:
//
//  1. A profile with no "mode:" header, or a line this cannot parse, is a
//     refusal. A check that read nothing must not report a pass.
//
//  2. A site that matched no statement at all is a refusal, not "n/a". Every
//     real function has statements, so zero means the profile does not cover
//     that package or resolve found the wrong file. Reporting it beside real
//     percentages is how a half-written profile gets read as a finished one.
//
//  3. A site with statements but none covered is exit 1, and that is a property
//     of the results rather than an error here. report decides it.
func measure(profile io.Reader, sites []Site) ([]Result, error) {
	results := make([]Result, len(sites))
	for i, s := range sites {
		results[i] = Result{Site: s}
	}

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

		file, start, stmts, count, err := parseBlock(text)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		for i := range results {
			s := &results[i]
			if s.File != file || start < s.Start || start > s.End {
				continue
			}
			s.Total += stmts
			if count > 0 {
				s.Covered += stmts
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !sawMode {
		return nil, fmt.Errorf("no \"mode:\" header, so this is not a coverage profile")
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
func parseBlock(text string) (file string, start, stmts, count int, err error) {
	f := strings.Fields(text)
	if len(f) != 3 {
		return "", 0, 0, 0, fmt.Errorf("%d fields, want 3 (block, statements, count): %q", len(f), text)
	}

	colon := strings.LastIndex(f[0], ":")
	if colon < 0 {
		return "", 0, 0, 0, fmt.Errorf("no \":\" separating the file from the block: %q", f[0])
	}
	file = f[0][:colon]

	from, _, ok := strings.Cut(f[0][colon+1:], ",")
	if !ok {
		return "", 0, 0, 0, fmt.Errorf("no \",\" separating the block's start from its end: %q", f[0])
	}
	lineText, _, ok := strings.Cut(from, ".")
	if !ok {
		return "", 0, 0, 0, fmt.Errorf("no \".\" separating the line from the column: %q", from)
	}
	if start, err = strconv.Atoi(lineText); err != nil {
		return "", 0, 0, 0, fmt.Errorf("start line %q: %w", lineText, err)
	}
	if stmts, err = strconv.Atoi(f[1]); err != nil {
		return "", 0, 0, 0, fmt.Errorf("statement count %q: %w", f[1], err)
	}
	if count, err = strconv.Atoi(f[2]); err != nil {
		return "", 0, 0, 0, fmt.Errorf("execution count %q: %w", f[2], err)
	}
	return file, start, stmts, count, nil
}
