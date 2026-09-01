// Command coupling measures data- and control-coupling coverage.
//
// Statement and branch coverage measure what happened INSIDE a component. They
// say nothing about whether two components interact correctly once integrated.
// A package at 95% statement coverage can have a caller that never exercises
// the one path its callee cares about, and no coverage number moves.
//
// DO-178C separates this from structural coverage for that reason. Objective
// 6.4.4.d asks for coverage of the interfaces between components, and DO-248C
// defines them:
//
//   - Data coupling: "The dependence of a software component on data not
//     exclusively under the control of that software component."
//   - Control coupling: "The manner or degree by which one software component
//     influences the execution of another software component."
//
// The mechanically useful part is narrow: a human declares the couplings, and
// this measures statement coverage restricted to them. The tool cannot discover
// what a coupling is.
//
// # What this does not claim
//
// No qualified Go compiler exists, so Go software does not achieve DO-178C's
// higher design assurance levels. This produces no certification evidence. It
// implements the measure, which is the best-defined measure of integration test
// adequacy available, in a language that has none.
//
// It also measures control coupling well and data coupling only by proxy: a
// registry row names a symbol, and a data couple is often a single field read.
// The output header says so.
//
// # Usage
//
//	go tool coupling [-registry FILE] [-profile FILE] [-format text|json]
//
// Exit codes carry the meaning, so never read a pipeline's status instead:
//
//	0  a number was produced, and every coupling has a covered statement
//	1  a coupling has no covered statement at all
//	2  the measure could not run
//
// There is no threshold, and there must not be one. A floor set before the
// number is understood is how a coverage gate gets set wrong.
package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Coupling is one row of the registry: an interface a human declared, or a
// package a human declared has none.
type Coupling struct {
	ID   string // stable identifier, referenced by the architecture document
	Kind string // "data" or "control"
	// Package is the FULL import path, not a path relative to the module root.
	// checkComplete compares it against module+"/"+dir and refuses a row that
	// does not match, which is the first thing an external caller gets wrong:
	// resolve would accept the relative form, and completeness will not.
	Package string
	Symbol  string // function or method name
	Lines   string // optional "start-end", to narrow the site within the symbol
	Note    string // what the coupling is, in one line
}

// Site is a Coupling resolved to a place in a file.
type Site struct {
	Coupling
	File  string // path as it appears in the coverage profile
	Start int    // first line of the site, inclusive
	End   int    // last line, inclusive
}

// Result is a Site with its coverage measured.
type Result struct {
	Site
	Covered int // statements executed at least once
	Total   int // statements in the site
}

// parseRegistry reads the tab-separated registry.
//
// A row with too few fields is a refusal rather than a skip. A registry that
// silently drops a malformed row reports a number for fewer couplings than it
// claims to cover, which is the failure this whole tool exists to prevent.
func parseRegistry(r io.Reader) ([]Coupling, error) {
	var out []Coupling
	sc := bufio.NewScanner(r)
	for line := 1; sc.Scan(); line++ {
		text := sc.Text()
		if trimmed := strings.TrimSpace(text); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		f := strings.Split(text, "\t")
		if len(f) < 4 {
			return nil, fmt.Errorf("line %d: %d fields, want at least 4 "+
				"(id, kind, package, symbol): %q", line, len(f), text)
		}

		c := Coupling{ID: f[0], Kind: f[1], Package: f[2], Symbol: f[3]}
		if len(f) > 4 {
			c.Lines = f[4]
		}
		if len(f) > 5 {
			c.Note = f[5]
		}

		switch c.Kind {
		case "data", "control":
		case "exempt":
			// A package with no coupling is a claim, and a claim belongs in the
			// registry where a reviewer sees it. Without this the check in §5
			// could only be a warning, and a warning that is not a gate is a
			// line people learn to scroll past.
			if c.Note == "" {
				return nil, fmt.Errorf("line %d: %s exempts %s with no reason given",
					line, c.ID, c.Package)
			}
		default:
			return nil, fmt.Errorf("line %d: kind %q, want \"data\", \"control\" or \"exempt\"", line, c.Kind)
		}
		if c.Lines != "" {
			if _, _, err := parseLineRange(c.Lines); err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
		}
		out = append(out, c)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the registry declares no couplings")
	}
	return out, nil
}

// parseLineRange reads a "start-end" narrowing from a registry row.
func parseLineRange(s string) (start, end int, err error) {
	lo, hi, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, fmt.Errorf("line range %q, want \"start-end\"", s)
	}
	if start, err = strconv.Atoi(lo); err != nil {
		return 0, 0, fmt.Errorf("line range %q: %w", s, err)
	}
	if end, err = strconv.Atoi(hi); err != nil {
		return 0, 0, fmt.Errorf("line range %q: %w", s, err)
	}
	if start > end {
		return 0, 0, fmt.Errorf("line range %q: start is after end", s)
	}
	return start, end, nil
}
