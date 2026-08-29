# Coupling coverage — design specification

- **Date**: 2026-08-29
- **Status**: specified, not scheduled. Target is v1, after the sweep ships.
- **Module**: `github.com/dd0wney/fault/tools` (separate module, `go 1.24.0`)
- **Command**: `go tool coupling`

This is Track B. It is a build-time tool, it will never enter the standard
library, and nothing in the `fault` module imports it.

## 1. The problem

Statement and branch coverage measure what happened **inside** a component. They
say nothing about whether two components interact correctly once integrated. A
package at 95% statement coverage can have a caller that never exercises the one
path its callee cares about, and no coverage number moves.

This is not a theoretical gap. In graphdb on 2026-08-28, nine of thirteen
defects found were coupling defects. Not one would have been caught by raising
statement or branch coverage inside a package.

## 2. What the standard actually asks for

DO-178C separates this from structural coverage deliberately. The relevant
objective is a single one:

| Objective | Ref. | Applies at | Output |
|---|---|---|---|
| Test coverage of software structure (data coupling and control coupling) is achieved | 6.4.4.d | DAL A, B, C | Software Verification Results (11.14) |

The objective requires independence at DAL A and B.

DO-248C supplies the definitions:

- **Data coupling**: "The dependence of a software component on data not
  exclusively under the control of that software component."
- **Control coupling**: "The manner or degree by which one software component
  influences the execution of another software component."

The measurement method is not left open. Rapita Systems' DO-178C verification
handbook (MC-WP-011) states it plainly:

> Typically, this will involve analyzing statement coverage of relevant
> read/write and procedure call statements.

And on what makes the analysis tractable:

> The key to efficient data coupling and control coupling coverage analysis is
> defining data coupling and control coupling interfaces with enough information
> with which to write test cases to test those interfaces.

That last sentence is the design. The tool cannot discover what a coupling is.
A human declares the couplings, and the tool measures coverage restricted to
them. Everything else follows from that split.

### 2.1 The limit of the claim

No qualified Go compiler exists, so Go software does not achieve DAL A, B or C.
This tool does not produce certification evidence and must never say that it
does. It implements the **measure**, which is the best-defined measure of
integration test adequacy available, in a language that has none.

## 3. Prior art: what `graphdb/scripts/dccc.sh` does

The bash original is the reference implementation, and the rewrite must keep its
behaviour except where §5 says otherwise.

1. Read a registry: `docs/internals/design/couplings.tsv`, one coupling per row
   — id, kind, package, symbol.
2. Resolve each symbol to a file and a line range, by scanning for
   `^func ... Name(` and then the next line that is exactly `}`.
3. Read a Go coverage profile.
4. Sum the profile's statement blocks whose start line falls inside a resolved
   range, splitting covered from total.
5. Print per-coupling and overall percentages.

Three of its behaviours are the valuable part, and all three are refusals:

| Condition | Exit | Why it refuses |
|---|---|---|
| A registry symbol resolves to nothing | 2 | A renamed function would be silently dropped, and the tool would report a number for fewer couplings than it claims. |
| A resolved site matches no statement in the profile | 2 | Zero statements is not a coverage result. It means the profile does not cover that package, or the resolver found the wrong file. Reporting it as "n/a" beside real percentages is how a half-written profile got read as a finished one. |
| A coupling has no covered statement at all | 1 | The interface was never exercised. This is the finding the objective exists to surface. |

**There is no threshold, and there must not be one.** A floor set before the
number is understood is how graphdb got its coverage floor wrong in #469. The
tool reports. A human decides.

## 4. Command

```
go tool coupling [-registry FILE] [-profile FILE] [-format text|json]
```

| Flag | Default | Meaning |
|---|---|---|
| `-registry` | `docs/couplings.tsv` | The coupling registry. |
| `-profile` | `coverage/coverage.out` | A Go coverage profile from `go test -coverprofile`. |
| `-format` | `text` | `json` for CI consumption. |

Exit codes carry the meaning, so a caller must never pipe the command into
another and read the pipeline's status:

| Exit | Meaning |
|---|---|
| 0 | A number was produced and every coupling has at least one covered statement. |
| 1 | A coupling has no covered statement at all. |
| 2 | The measure could not run. See the refusals in §3. |

## 5. What the Go rewrite changes

Four defects in the bash version are worth fixing, and one gap is worth naming.

**5.1 Resolve symbols with `go/parser`, not with a regular expression.**
The awk pattern matches only a top-level `func` whose closing brace sits at
column zero. It misses anything gofmt formats differently, and it silently
returns "not found", which the tool then reports as a registry error against
correct code. `go/parser` and `go/ast` are in the standard library, so this
costs no dependency. A resolved symbol gets an exact `token.Position` range.

**5.2 Match files exactly, not by substring.**
The bash version tests `index(path, pkg "/" base)`. That matches
`pkg/storage2/x.go` when the registry names `pkg/storage`. The rewrite compares
resolved absolute paths.

**5.3 Support a coupling that is not a whole function.**
The registry can only name a symbol, and the tool then measures the whole
function body. A data couple is often a single field read. The registry gains an
optional line range so a row can name a narrower site.

**5.4 Measure data coupling, not only control coupling.**
This is the real gap, and it must be stated in the tool's own documentation
rather than hidden. DO-248C defines data coupling in terms of **data**, and the
current registry can only name functions. Measuring statement coverage of a
function that touches shared data is a proxy for the data couple, not the data
couple itself. The rewrite keeps the proxy — it is what the handbook's "relevant
read/write and procedure call statements" describes — and says so in its output
header, so nobody mistakes the number for something stronger.

## 6. Registry format

Tab-separated, `#` for comments. One coupling per row.

| Column | Required | Meaning |
|---|---|---|
| `id` | yes | Stable identifier, referenced by the architecture document. |
| `kind` | yes | `data` or `control`. |
| `package` | yes | Import path relative to the module root. |
| `symbol` | yes | Function or method name. |
| `lines` | no | `start-end`, to narrow the site within the symbol (§5.3). |
| `note` | no | What the coupling is, in one line. |

The registry is the architecture description, in machine-readable form. It is
reviewed like source, and a row added without a note is incomplete.

## 7. The tool must be able to report the opposite result

Required, per `verify-the-instrument`. The bash original ships
`scripts/dccc-selftest.sh`, and the rewrite ships the equivalent as ordinary Go
tests. Build a fixture for each way the check should fail, and assert the exit
code:

| Fixture | Expected exit |
|---|---|
| A registry naming a symbol that no file defines | 2 |
| A registry that resolves, with a profile covering a different package | 2 |
| A coupling whose statements are all uncovered | 1 |
| A coupling whose statements are all covered | 0 |
| A registry with only comment lines | 2 |
| A profile that is empty, and a profile that is truncated mid-line | 2 |

A gate with no fixture for its own failure modes is not a gate. Two of the
thirteen defects graphdb found on 2026-08-28 were in the test apparatus rather
than in the system, and one of them was a coverage floor taken from the wrong
machine.

## 8. Relationship to the rest of the project

| Piece | Relationship |
|---|---|
| `fault` | None. The core never imports this, and this never imports the core. |
| `fault/tools` contract digests | A sibling command in the same module. Same shape: a checked-in registry, a refusal when the registry and the code disagree. |
| Mutation testing | Deliberately excluded. `go-gremlins/gremlins` already exists and is active, and mutation testing needs an external binary, which is why it can never ship as a library. |
| Per-package coverage floors | A sibling command. `vladopajic/go-test-coverage` already does this publicly, so it is the lowest-value item in the module. |

## 9. Why this is worth building at all

Market research found this measure and the contract digest to be the only two
items in the whole project with **no public Go equivalent**. Mutation testing has
three live tools. Per-package coverage floors have one. Coupling coverage has
none.

The audience is narrow — teams who already think in terms of DO-178C, or who
have been bitten by an integration defect that full statement coverage did not
catch. That is a smaller audience than the sweep's, which is why this ships
second.
