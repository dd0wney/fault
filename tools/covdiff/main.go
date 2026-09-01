package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// Exit codes. They carry the meaning, so a caller must read the status of this
// command and never a pipeline's.
const (
	exitWrote      = 0 // a profile was written
	exitCannotTell = 2 // the subtraction could not be trusted
)

func main() {
	baseline := flag.String("baseline", "", "the normal range profile, subtracted from -profile")
	prof := flag.String("profile", "", "the profile to subtract from, usually the whole suite")
	out := flag.String("o", "", "write here instead of stdout")
	flag.Parse()

	os.Exit(run(*baseline, *prof, *out, os.Stdout, os.Stderr))
}

func run(baselinePath, profilePath, outPath string, stdout, stderr io.Writer) int {
	if baselinePath == "" || profilePath == "" {
		fmt.Fprintln(stderr, "covdiff: -baseline and -profile are both required")
		fmt.Fprintln(stderr, "covdiff: go tool covdiff -baseline normal.out -profile merged.out -o robustness.out")
		return exitCannotTell
	}

	baseline, err := open(baselinePath)
	if err != nil {
		fmt.Fprintf(stderr, "covdiff: the baseline: %v\n", err)
		return exitCannotTell
	}
	profile, err := open(profilePath)
	if err != nil {
		fmt.Fprintf(stderr, "covdiff: the profile: %v\n", err)
		return exitCannotTell
	}

	w := stdout
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			fmt.Fprintf(stderr, "covdiff: %v\n", err)
			return exitCannotTell
		}
		defer func() { _ = f.Close() }()
		w = f
	}

	if err := subtract(baseline, profile, w); err != nil {
		fmt.Fprintf(stderr, "covdiff: %v\n", err)
		return exitCannotTell
	}
	return exitWrote
}

func open(path string) (*profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parse(f)
}
