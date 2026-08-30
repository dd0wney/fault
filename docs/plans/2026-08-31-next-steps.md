# Next Steps — 2026-08-31

Supersedes `docs/plans/2026-08-30-next-steps.md`, which stays for the full text
of the six `fault/sql` forks. Tasks 2 and 3 in that document are done and
merged.

## State, measured this session

| Fact | Value | How it was read |
|---|---|---|
| `origin/main` | `5671d93` | `git ls-remote origin main` |
| CI on that SHA | success | `gh run list --branch main --json conclusion` |
| Open pull requests | none | `gh pr list --state open` |
| Local `main` and `fix/dep-gate-test-imports` | both `d78c89f`, **8 behind** | `git branch -vv` |
| Working tree | 2 modified scripts, 1 untracked fixture, `.handoff.md` | `git status` |
| The gate today | `108 packages, standard library only`, exit 0 | `./scripts/no-external-deps.sh` |
| The selftest today | 6 rows pass, the new row fails `exit 0, want 1` | `./scripts/no-external-deps-selftest.sh` |

The failing row is the point. Exit 0 is the false pass this branch removes.

## A shape TODO 3 does not name

TODO 3 in `scripts/no-external-deps.sh` names two shapes that `-test` adds and
that the `^MODULE(/|$)` anchor cannot read: `pkg [pkg.test]` and `pkg.test`.
There is a third, and it is the one that survives the obvious fix.

Measured on this module: `go list -deps -test ./...` gives 148 lines. Strip the
trailing ` [context]`, drop every line ending `.test`, and 140 unique paths
remain. Exactly one of them still reads as external under the current filter:

```
github.com/dd0wney/fault_test
```

That is the root package's external test package. `_` follows the module path
directly, so neither `/` nor end-of-line follows, and the anchor rejects it.
The sub-package form (`github.com/dd0wney/fault/alloc_test`) is safe, because
the `/` arrives first. Only the root package has this shape, so a fix tested
against `./crash/` alone would look complete and would not be.

The fixture has the mirror of the same problem. Against
`scripts/testdata/testonly-external`, `-deps -test` reports four `example.com`
lines and only one of them is the finding:

```
example.com/ext                                    <- the real finding
example.com/depfixture                             <- the fixture's own module
example.com/depfixture [example.com/depfixture.test]
example.com/depfixture.test
```

So the fixture would exit 1 for one right reason and three wrong ones while a
hardcoded `MODULE` stays in the script. A fixture that goes green while proving
nothing is the failure this branch exists to remove, so TODO 1 and TODO 3 must
land together or in that order.

Confirmed alongside: the fixture resolves under `GOPROXY=off` (exit 0), so CI
needs no network. `gofmt -l .` is clean with the fixture present, `go vet ./...`
is clean, and `go list ./...` does not reach `testdata`.

---

## 0. Rebase the branch onto `origin/main`

Not a code commit. `fix/dep-gate-test-imports` sits on `d78c89f` and misses the
`lint` job, `.golangci.yml`, the `tools` baseline and the crash assertions from
pull requests #5, #6 and #7.

**Acceptance**

- `git log --oneline -1 origin/main` is the branch's merge base.
- `.golangci.yml` and `.github/workflows/ci.yml`'s `lint` job are present in the
  working tree afterwards.
- `go test -count=1 ./...` passes after the rebase.

---

## Group A — the dependency gate reads test imports

One pull request, three commits. Commit 1 is red on purpose: CI judges the head
of a pull request, not each commit, and the house rule is the failing test
first.

`fault/sql` is where an import of a real driver would arrive, so this lands
before Group C rather than after it.

### 1. The fixture and the failing selftest row

Both are written and uncommitted. This commit only records them.

- [ ] `scripts/testdata/testonly-external/` — module, stub, `replace`.
- [ ] `scripts/no-external-deps-selftest.sh` — the `expect_root` helper and the
      one row that uses it.

**Acceptance**

- The selftest exits 1, and the message reads `exit 0, want 1`.
- The recorded before-value is 0, written into the commit message as the false
  pass being removed.
- `gofmt -l .` stays empty and `go vet ./...` stays clean.

### 2. `--root DIR`, and `MODULE` derived rather than hardcoded

TODO 1 and TODO 2. Both are about aiming the gate somewhere other than here.

- [ ] `--root DIR`, parsed in the shape `scripts/mutation.sh` already uses: a
      `while [ $# -gt 0 ]` loop with a `case "$1"`.
- [ ] `MODULE` from `go list -m` inside `ROOT`.
- [ ] `-` on stdin still works, and still uses this module's path.

**Acceptance**

- `./scripts/no-external-deps.sh --root scripts/testdata/testonly-external`
  names `example.com/ext` and does **not** name `example.com/depfixture`.
- A `--root` pointing at a directory that is not a module exits **2**, never 0
  and never 1. A check that could not run must not report a pass.
- The six stdin rows of the selftest still pass unchanged.
- The row from commit 1 still fails, because `-test` is not there yet.

### 3. `-deps -test`, and the list normalised

TODO 3. Three decisions, and the plan takes none of them:

1. Strip the trailing ` [context]`, or widen the anchor to accept it.
2. Drop the `.test` binaries, or match them.
3. Handle `MODULE_test` — the third shape, named above.

**Acceptance**

- `./scripts/no-external-deps.sh` exits 0 on this module and prints a count
  **above 108**. Under strip-and-drop the number is 140, and a different policy
  gives a different number. Record whichever the chosen policy gives.
- The selftest passes with all seven rows listed.
- `github.com/dd0wney/fault_test` appears in no output as external. Test this
  against the whole module, not one package: only the root package has the
  shape.
- The comment block in the script is replaced by what the code now does. A TODO
  that outlived its change is a false statement in the file.

### 4. Gate the `tools` module too

New, and cheap once `--root` exists. `tools/go.mod` requires nothing outside the
standard library today, and no gate says so. This is the same gap that
`tools/mutation-baseline.tsv` closed for mutation testing in pull request #6.

- [ ] One step in the `tools` job: `./scripts/no-external-deps.sh --root tools`.

**Acceptance**

- The exit code and the package count are recorded from a run, not predicted.
- If `tools` does not pass, the finding is written down and the step still
  lands — a gate is not deferred because it has something to say.

---

## Group B — `crash.Recorder.Observed()`

Task 5b of the previous plan. **Recommended before task 5**, not after it. See
the ordering note at the end.

### 5. `Observed()`, and the limit written into `crash/doc.go`

The total bypass is already refused: `crash/run.go:54` returns `errNoMutations`.

**CORRECTED 2026-08-31, after measuring it.** The claim this paragraph carried
was that a partial bypass is silent. That is wrong, and the correction narrows
the task rather than removing it. Four shapes, all measured against `plan`:

| The bypass | `plan` |
|---|---|
| adds a name under the record root | **refuses** — the replay control catches it |
| writes a name under the root, then removes it | passes, silent |
| writes outside the record root | passes, silent |
| rewrites a recorded path with identical bytes | passes, silent |

So the honest statement is that a bypass which leaves the recorded tree
IDENTICAL is silent. `checkReplay` compares the whole replayed record with
`readTree(root)`, so anything that changes that tree is already loud. The third
row is the ordinary shape and the live graphdb one, because the control reads
the record root alone and never looks anywhere else.

This is the plan's own instance of the failure it was written about: a claim
about code, believed without opening the code. The paragraph was accurate about
`errNoMutations` and wrong about what the OTHER guard already covered.

The precedent is `fs.Outstanding()` from pull request #4. Expose the fact, and
let the caller assert. The library must not decide what "reached" means.

- [ ] `Observed()` returns the paths the recorder actually served.
- [ ] A test for the partial case: one path through the seam, one around it.
- [ ] `crash/doc.go` states that a path outside the seam is unobserved, and that
      the total case fails while the partial case does not.

**Acceptance**

- The partial-bypass test fails before the change and passes after it.
- `crash/doc.go` reads as plainly as the limits already there.
- The live graphdb case works: a caller can assert that the recorder saw
  `index.lsa`.

### 6. The coupling row and the re-read floor

Separate commit, because it is bookkeeping and it has its own failure mode.

- [ ] A `docs/couplings.tsv` row for the new interface.
- [ ] Re-run `scripts/mutation.sh ./crash/` and re-record the floor.

**Acceptance**

- The coupling gate exits 0 with the new row exercised.
- The `./crash/` floor is re-read from a run after the code lands. The recorded
  figure is 0.776639 against a floor of 0.77, which leaves 0.0066 — about three
  new survivors turn it red. A stale reference value is not a broken gate, and
  nothing in the gate can detect one.

---

## Group C — `fault/sql`

### 7. The design document

Task 5 of the previous plan, unchanged. The six forks and their full statements
are in `docs/plans/2026-08-30-next-steps.md`:

1. `driver.ErrBadConn` — `database/sql` retries twice, so one armed point
   becomes several attempts and the count stops meaning anything.
2. The pool — the N-th operation is stable only under `SetMaxOpenConns(1)`.
3. The optional interfaces — 28 of them, and a wrapper that implements fewer
   than the driver beneath it moves the sequence silently.
4. What counts as one operation — `Rows.Next` per row would exceed `maxOps`
   (512) on a 600-row query, and non-termination reads exactly like a defect in
   the code under test.
5. The invariant and the leak count — `sql.DB.Stats().OpenConnections` is the
   analogue of `fs.Outstanding()`.
6. The test driver — the dependency gate forbids a real one, so the tests need a
   `driver.Driver` written by hand inside the module.

Fork 5 is easier to answer after task 5 lands, which is the ordering argument
below.

**Acceptance**

- `docs/design/<date>-sql-adapter.md` exists.
- Each fork has an answer and a reason.
- The limits section is as plain as `crash/doc.go`.

### 8–14. Implementation

Not writable until the design answers the forks. Probable shape, carried over
unchanged: the seam (`Driver`, `Connector`, `Conn`), statements and
transactions, rows and the `maxOps` answer, optional-interface forwarding,
the hand-written test driver, `doc.go` plus the `docs/couplings.tsv` rows, and
the baseline row plus the README table.

Task 14 is not optional. `mutation.sh` exits 2 for a package the baseline omits,
so `fault/sql` cannot land without its row.

---

## Group D — the graphdb sweep

### 15. Correct the mmap harness, then re-run it

The harness at `/mnt/ssd2/Workspace/github.com/fault-graphdb-sweep/` does not
discriminate. Both builds pass:

| graphdb build | states | 0 nodes | 3 nodes | verdict |
|---|---|---|---|---|
| `2df539b` (fixed) | 26 | 12 | 14 | pass |
| `e418768` (before #530) | 43 | 29 | 14 | pass |

The model saw the defect. The broken build produces 17 extra states, each a lost
un-synced rename, and the invariant threw the signal away: with a single
publish, "the rename was never durable" and "the crash landed before the
publish" both leave zero nodes, and zero was accepted as legal.

- [ ] Two publishes, so the two causes of an empty tree stop sharing a state.
- [ ] A rule scoped by crash point, read out of `t.Name()`.

**Acceptance**

- The two builds **disagree**. Until they do, the invariant is wrong, not the
  code. This defect has now appeared three times in three artefacts in one day —
  once too strict, twice too permissive — so treat any crash-sweep invariant as
  wrong until two builds disagree about it.
- The harness names both builds in its output.
- Any finding carries a test that failed first, then a merged pull request.
- A clean result carries a positive control beside it.

Do not report the current numbers. They are the harness talking about itself.

---

## Order

Recommended: **0 → 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8–14**, with 15 whenever the
graphdb session is quiet.

This moves `Observed()` (5 and 6) ahead of the `fault/sql` design (7). The
previous plan scheduled it after.

**For the swap.** `Observed()` is small, fully specified, and blocked on
nothing. It has a live external consumer waiting on it. Its shape is the
precedent that fork 5 of the design will cite, so answering the design fork gets
easier once the code exists rather than harder.

**Against it.** The design document is the larger unknown, and a long unknown
usually earns the earlier start. The `fault/sql` work also unblocks seven
implementation tasks, so a delay there delays more.

**Why the swap wins anyway.** The design is one long session, not several. It
does not parallelise, and starting it earlier does not finish it earlier. Two
small merged commits ahead of it cost about one session and remove a real gap
that another repository is already waiting on.

Group A stays first in either case. It is the gate that guards the exact import
`fault/sql` would introduce.
