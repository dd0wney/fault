package main

import (
	"fmt"
	"io"
)

// measure attributes a Go coverage profile to the resolved sites.
//
// A profile line looks like this, after the "mode:" header:
//
//	github.com/dd0wney/fault/fs/fault.go:57.75,58.18 1 4
//	 path                        :start.col,end.col  stmts count
//
// TODO(ddowney): the attribution.
//
// For each block, find the sites whose file matches and whose range contains
// the block's start line. Add the statement count to that site's Total, and to
// its Covered as well when the execution count is greater than zero.
//
// Three refusals, and they are the valuable part of the tool.
//
//  1. A profile that is empty or has no "mode:" header is exit 2. A check that
//     read nothing must not report a pass.
//
//  2. A site that matched no statement at all is exit 2, not "n/a". Every real
//     function has statements, so zero means nothing matched: the profile does
//     not cover that package, or resolve found the wrong file. Reporting it
//     beside real percentages is how a half-written profile got read as a
//     finished one.
//
//  3. A site with statements but none covered is exit 1. That interface was
//     never exercised, and it is the finding the DO-178C objective exists to
//     surface.
//
// This function reports the first two by returning an error. The third is a
// property of the results, and report decides it.
//
// Match the file exactly. The bash original used a substring test and matched
// pkg/storage2 when the registry named pkg/storage.
func measure(profile io.Reader, sites []Site) ([]Result, error) {
	return nil, fmt.Errorf("measure: %w", errNotWritten)
}
