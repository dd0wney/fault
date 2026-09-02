# Measuring data and control coupling coverage in your own module

`go tool coupling` measures statement coverage restricted to the component
interfaces you declare. `docs/design/2026-08-29-coupling-coverage.md` explains
what the measure is and where it comes from. This page is the other half: how to
point it at your code.

Every command and every number below was run against a throwaway module on
2026-09-01, and two defects in the tool were found by doing it. Both are fixed.
They are named here because each one is still the mistake you will make first.

---

## What it does not claim

Read this before the rest, because it bounds everything after it.

No qualified Go compiler exists, so Go software does not achieve DO-178C's
higher design assurance levels. **This produces no certification evidence and
must never be described as though it does.** It implements the *measure*, which
is the best-defined measure of integration test adequacy available, in a
language that has none.

It also measures control coupling directly and data coupling **by proxy**: a
registry row names a symbol, and a data couple is often a single field read. The
tool's own output header says so on every run.

And it is one structural measure, not the set. DO-178C 6.4.4 asks for statement
coverage, decision coverage, modified condition/decision coverage (MC/DC, at
DAL A, objective 6.4.4.c), and data- and control-coupling coverage (6.4.4.d).
This tool measures the last. Go's `-covermode` counts statements, not
conditions, and no MC/DC tool for Go exists, so **this project does not meet
6.4.4.c and does not claim to**. A reader who sees a coupling figure and no
mention of MC/DC could infer the structural set is complete; it is not, and
this paragraph exists so that no document here reads as though it were.

---

## 1. Get the tool

```
go get -tool github.com/dd0wney/fault/tools/coupling
```

This adds a `tool` directive to your `go.mod`. It is a build-time tool in a
separate module, and nothing in your program links against it.

## 2. Declare the couplings

The registry is the architecture description in machine-readable form. The tool
cannot discover what a coupling is — a human declares them, and the tool
measures coverage restricted to what was declared.

`docs/couplings.tsv`, tab-separated, `#` for comments:

```
# id	kind	package	symbol	lines	note
C1	control	example.com/demo/app	Save		app decides whether store.Write runs, and branches on what it returns
C2	data	example.com/demo/store	Write		store depends on an FS it does not own
```

| Column | Required | Meaning |
|---|---|---|
| `id` | yes | Stable identifier. Reference it from your architecture document. |
| `kind` | yes | `data` or `control`. |
| `package` | yes | **The full import path.** See below. |
| `symbol` | yes | Function or method name. `Type.Method` for a method. |
| `lines` | no | `start-end`, to narrow the site inside the symbol. |
| `note` | no | What the coupling is, in one line. A row without one is incomplete. |

**The package column is the FULL import path, not a path relative to the module
root.** `example.com/demo/store`, not `store`. This is the first thing that goes
wrong: `resolve` accepts the relative form and the completeness check refuses it,
so the two halves of the tool disagreed and the design document named the wrong
one. Fixed on 2026-09-01, after it cost the first attempt at this page.

### Every package needs a row or an exemption

The tool walks your module and refuses when a package appears in neither:

```
coupling: these packages appear in neither a coupling row nor an exemption, so
the gate would report a pass having measured nothing about them:
example.com/demo/app, example.com/demo/store. Add a row, or an "exempt" row
saying why the package has no coupling
```

That refusal is the point. The registry is data the gate reads to decide what to
look at, and data drifts from the thing it describes. Add a package and it
escapes the measure entirely while the gate reports a pass, having measured
exactly what it was told, when what it was told had gone stale.

An exemption is a row, not a silence, so a reviewer sees the claim and its reason
in the diff that introduces it. It needs a note.

## 3. Produce a coverage profile — with `-coverpkg`

```
go test -coverpkg=./... -coverprofile=coverage.out ./...
```

**The `-coverpkg=./...` is not optional, and leaving it out is the second thing
that goes wrong.**

Without it, `go test` instruments only the package under test. A coupling site in
a package whose statements are executed from *another* package's tests then
matches zero covered statements, and the tool exits 1 with a finding that is not
real. Measured on the demo module:

```
without -coverpkg    C2  example.com/demo/store.Write     0/12     0.0%   exit 1
with    -coverpkg    C2  example.com/demo/store.Write    12/12   100.0%   exit 0
```

You only avoid this if every package in your module has its own tests. That is
true of the `fault` module and it is why the defect below survived so long.

> A profile from `-coverpkg` contains one record per block **per test binary** —
> 5117 lines for 731 distinct blocks across seven packages. Until 2026-09-01 the
> tool summed them, and reported 20.7% for a tree that measures 98.6%. If you are
> pinned to an older version, upgrade before you believe a number.

## 4. Run the tool

```
go tool coupling -root . -registry docs/couplings.tsv -profile coverage.out
```

```
Statement coverage restricted to declared coupling sites.
Control coupling is measured directly. Data coupling is measured by
proxy: a row names a symbol, and a data couple is often one field read.

   covered          id   kind     site
     4/5     80.0%  C1   control  example.com/demo/app.Save
    12/12   100.0%  C2   data     example.com/demo/store.Write

  16/17 statements across 2 coupling sites = 94.1%
```

`-format json` for CI.

That `4/5` is a real result, not noise. `app.Save` has an early return for empty
input, and the sweep driving the tests never passes an empty record. One
statement of a declared control coupling has never executed. Finding that is the
entire objective.

## 5. Read the exit code — the tool's, not a pipeline's

| Exit | Meaning |
|---|---|
| 0 | A number was produced, and every coupling has at least one covered statement. |
| 1 | A coupling has no covered statement at all. The interface was never exercised. |
| 2 | The measure could not run. A registry symbol resolved to nothing, a site matched no statement, the profile was unreadable. |

**Never pipe the command into another and read the pipeline's status.** In a
shell pipeline `$?` is the last stage's, so

```sh
go tool coupling ... | tail -4 ; echo $?     # WRONG: this is tail's status
```

reports 0 for a run that exited 1. This is not hypothetical advice: it happened
while writing this page, and the reading "exit 0" sat in a verification
transcript next to the tool's own output saying `1 coupling site(s) have no
covered statement at all`.

Verified without a pipe:

```
no -coverpkg, C2 uncovered      exit 1
-coverpkg, everything covered   exit 0
profile does not exist          exit 2
```

## 6. There is no threshold, and there must not be one

The tool reports. A human decides.

A floor set before the number is understood is how a coverage gate gets set
wrong. Wire the exit code into CI if you want a gate, but choose what to do about
a `1` by reading the row, not by picking a percentage in advance.

---

## 7. Robustness coverage: which interfaces had their *error* paths executed

Everything above measures whether a declared interface was exercised. It does not
say whether it was exercised **under failure**, and the difference is the whole
reason a fault-injection library is involved.

DO-178C 6.4.2.2 asks for two kinds of test case: normal range, and robustness —
"behaviour when abnormal conditions and faults are present". A `fault.Sweep`
produces both in one loop: for a K-operation scenario it yields K+1 passes, one
failing each operation in turn plus a terminating pass that fails none. That last
pass **is** the normal range case.

`go test -coverprofile` accumulates across the binary, so the profile merges
them. Measured on a three-package demonstration module:

| Site | normal only | merged | robustness only |
|---|---|---|---|
| `app.Save` | 4/7 57.1% | 5/7 71.4% | 1/7 14.3% |
| `store.Write` | 6/12 50.0% | **12/12 100.0%** | 6/12 50.0% |
| `config.Limit` | 1/1 100.0% | **1/1 100.0%** | **0/1 0.0%** |

`store.Write` and `config.Limit` are indistinguishable in the merged column —
both 100.0%, exit 0. One has had its error handling exercised exhaustively. The
other has none exercised at all. The objective's letter is satisfied identically
by both.

### Getting the third column

Write a normal range test alongside the sweep. A zero `fault.Points` arms
nothing, so every operation succeeds:

```go
func TestNormalRange(t *testing.T) {
	fsys := faultfs.New(&fault.Points{}, faultfs.OS())
	if _, err := app.Save(fsys, t.TempDir(), []byte("record")); err != nil {
		t.Fatalf("the normal range case must not fail: %v", err)
	}
}
```

That is not an extra cost this page invents. It is the other half of 6.4.2.2,
which the standard asks for anyway.

Then:

```sh
go test -run TestNormalRange -coverpkg=./... -coverprofile=normal.out ./...
go test                      -coverpkg=./... -coverprofile=merged.out ./...
go tool covdiff  -baseline normal.out -profile merged.out -o robustness.out
go tool coupling -root . -registry docs/couplings.tsv -profile robustness.out
```

`covdiff` writes a profile in which a block is covered only when `merged` covered
it and `normal` did not. The result is an ordinary coverage profile, so the same
`coupling` command reads it — the measure needs no new measuring tool.

```
     1/7     14.3%  C1   control  example.com/demo/app.Save
     6/12    50.0%  C2   data     example.com/demo/store.Write
     0/1      0.0%  C3   data     example.com/demo/config.Limit

  1 coupling site(s) have no covered statement at all.
```

Exit 1, naming `C3`: a declared interface whose error path never executed.

### The refusal that matters most

`covdiff` refuses a baseline that covered nothing:

```
covdiff: the baseline covers no block at all, so subtracting it changes nothing
and every coupling would report its whole coverage as reached only under
injection. A normal range run that did not build, or a -run pattern that matched
no test, produces exactly this
```

Without that refusal, a `-run` pattern matching no test yields a profile of
zeroes, the subtraction becomes a no-op, and **every** interface reports its full
coverage as robustness-only. That reads as "every error path is exercised" — a
green report manufactured by a control that did not run. It was reproduced with a
real `go test -run TestNoSuchTest` while writing this section.

---

## The whole thing, in one block

```sh
go get -tool github.com/dd0wney/fault/tools/coupling
$EDITOR docs/couplings.tsv                                   # a row per coupling
go test -coverpkg=./... -coverprofile=coverage.out ./...      # note -coverpkg
go tool coupling -root . -registry docs/couplings.tsv -profile coverage.out
echo $?                                                       # 0, 1 or 2
```

And for robustness coverage — which interfaces had their error paths executed:

```sh
go test -run TestNormalRange -coverpkg=./... -coverprofile=normal.out ./...
go test                      -coverpkg=./... -coverprofile=merged.out ./...
go tool covdiff  -baseline normal.out -profile merged.out -o robustness.out
go tool coupling -root . -registry docs/couplings.tsv -profile robustness.out
echo $?
```
