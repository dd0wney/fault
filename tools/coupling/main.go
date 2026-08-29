package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

// Exit codes. They carry the meaning, so a caller must read the status of this
// command and never a pipeline's.
const (
	exitCovered   = 0 // a number was produced, and every coupling has a covered statement
	exitUncovered = 1 // a coupling has no covered statement at all
	exitCannotRun = 2 // the measure could not run
)

func main() {
	registry := flag.String("registry", "docs/couplings.tsv", "the coupling registry")
	profile := flag.String("profile", "coverage/coverage.out", "a Go coverage profile")
	format := flag.String("format", "text", "text or json")
	root := flag.String("root", ".", "the module root to resolve symbols against")
	flag.Parse()

	os.Exit(run(*root, *registry, *profile, *format, os.Stdout, os.Stderr))
}

func run(root, registry, profile, format string, stdout, stderr io.Writer) int {
	rf, err := os.Open(registry)
	if err != nil {
		fmt.Fprintf(stderr, "coupling: %v\n", err)
		return exitCannotRun
	}
	defer rf.Close()

	couplings, err := parseRegistry(rf)
	if err != nil {
		fmt.Fprintf(stderr, "coupling: %s: %v\n", registry, err)
		return exitCannotRun
	}

	if err := checkComplete(root, couplings); err != nil {
		fmt.Fprintf(stderr, "coupling: %v\n", err)
		return exitCannotRun
	}

	sites, err := resolve(root, couplings)
	if err != nil {
		fmt.Fprintf(stderr, "coupling: the registry and the code disagree: %v\n", err)
		return exitCannotRun
	}

	pf, err := os.Open(profile)
	if err != nil {
		fmt.Fprintf(stderr, "coupling: %v\n", err)
		fmt.Fprintf(stderr, "coupling: generate it with 'go test -coverprofile'\n")
		return exitCannotRun
	}
	defer pf.Close()

	results, err := measure(pf, sites)
	if err != nil {
		fmt.Fprintf(stderr, "coupling: %s: %v\n", profile, err)
		return exitCannotRun
	}

	return report(stdout, results, format)
}

// report prints the results and returns the exit code.
//
// There is no threshold. A coupling with statements but none covered is a
// finding, and a coupling with any covered statement is not: the objective asks
// whether the interface was exercised, not how thoroughly.
func report(w io.Writer, results []Result, format string) int {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return exitCannotRun
		}
	} else {
		fmt.Fprintln(w, "Statement coverage restricted to declared coupling sites.")
		fmt.Fprintln(w, "Control coupling is measured directly. Data coupling is measured by")
		fmt.Fprintln(w, "proxy: a row names a symbol, and a data couple is often one field read.")
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  %8s  %6s  %-4s %-8s %s\n", "covered", "", "id", "kind", "site")
	}

	var covered, total, uncovered int
	for _, r := range results {
		covered += r.Covered
		total += r.Total
		if r.Covered == 0 {
			uncovered++
		}
		if format != "json" {
			fmt.Fprintf(w, "  %4d/%-4d %5.1f%%  %-4s %-8s %s.%s\n",
				r.Covered, r.Total, 100*float64(r.Covered)/float64(r.Total),
				r.ID, r.Kind, r.Package, r.Symbol)
		}
	}

	if format != "json" {
		fmt.Fprintf(w, "\n  %d/%d statements across %d coupling sites", covered, total, len(results))
		if total > 0 {
			fmt.Fprintf(w, " = %.1f%%", 100*float64(covered)/float64(total))
		}
		fmt.Fprintln(w)
		if uncovered > 0 {
			fmt.Fprintf(w, "\n  %d coupling site(s) have no covered statement at all.\n", uncovered)
		}
	}

	if uncovered > 0 {
		return exitUncovered
	}
	return exitCovered
}
