# Next Steps — 2026-08-30

**State at writing:** four PRs merged, none open, `main` green at `d78c89f`.
277 tests and subtests across five packages plus the `tools` module.

**Updated 2026-08-30 19:35.** Tasks 2 and 3 are done and merged. `main` is at
`2b62ced` with six PRs merged. Task 1 is prepared and unwritten. Task 5b was
added after this plan was approved. Remaining order: **1 → 5 → 5b → 6-12**,
with **4** whenever graphdb is quiet.

---

## Two findings that predate this plan

Both were measured this session and neither is in `.handoff.md`.

**The dependency gate cannot see a test-only import.** `no-external-deps.sh`
runs `go list -deps ./...`, which excludes test imports. Control: a scratch
module whose only external import sits in a `_test.go` file yields 0 lines for
`net/http/httptest` under `-deps` and 12 under `-deps -test`. The selftest does
not catch it, because it pipes a list to the gate on stdin — it exercises the
filter, never the `go list` invocation that builds the list. This is the
"ask what the gate actually read" failure in its purest form: the gate is
correct about the graph it was handed, and the graph is the wrong one.

**graphdb has more positional call sites than the handoff records.** The
handoff says three. Outside `_test.go`:

| Call | Sites |
|---|---|
| `WriteAt` | `pkg/storage/mmap_snapshot_writer.go:184`, `pkg/btree/pager.go:122`, `pkg/btree/pager.go:165` |
| `Seek` | `pkg/wal/wal.go:138`, `pkg/wal/wal.go:217`, five in `pkg/lsm/` |

Three further sites live in `pkg/vfs/vfstest/crash.go`, which is test support
rather than a target.

---

## Group A — the gates

Three commits. Each repairs a check that does not run, or one that does not read
enough to mean what it claims.

### 1. The dependency gate reads test imports

`fault/sql` is exactly where an import of a real driver would enter, so this
lands before Group C, not after it.

- [ ] `scripts/no-external-deps.sh` calls `go list -deps -test ./...`.
      Not one word: `-test` adds `pkg [pkg.test]` and `pkg.test`, and both
      defeat the `^MODULE(/|$)` anchor, so the gate then reports this
      module's own packages as external. Measured: 108 packages become 148.
- [ ] A `--root DIR` flag, in the shape `scripts/mutation.sh` already uses. The
      selftest needs it to aim the gate at a fixture directory.
- [ ] A fixture module under `scripts/testdata/` whose only external import is
      in a `_test.go` file. A local stub module plus a `replace` directive, so
      the fixture needs no network.

**Acceptance**

- The new fixture exits 1 after the change, and the exit code **before** the
  change is recorded. It is 0, and that 0 is the false pass this task removes.
- `./scripts/no-external-deps-selftest.sh` passes and lists the new fixture.
- `./scripts/no-external-deps.sh` still reports standard library only.
- The package count in its output rises, because test imports now count.

### 2. The `golangci-lint` job — DONE, PR #5 (`f616ecc`, `fa8391e`, `2b62ced`)

`CLAUDE.md` asserts this gate. It runs nowhere: `ci.yml` has `test`, `checks`,
`tools` and `mutation`, and none invokes it.

- [ ] `.golangci.yml`. No config file exists today.
- [ ] A CI job using the official action, which pins a binary built with the
      toolchain CI selects.
- [ ] A step proving the job can report a violation — the house rule that every
      gate ships a selftest that runs first.
- [ ] The local breakage written down beside the job: binary 2.12.2 built with
      go1.26.3 cannot typecheck a go1.27.0 standard library
      (`internal/poll/splice_linux.go:237: unknown field rfd`), so the failure
      is the toolchain pairing and not this repository's code.

**Acceptance**

- The job fails on a seeded violation.
- The job passes on `main` as it stands, or every finding is recorded.

**Decided — the linter set.** The standard five, plus `errorlint`, plus
`errcheck`'s `check-type-assertions`. `check-blank` is OFF, and the cause is a
measurement: with it on, `errcheck` reports 55 findings, and 42 of them are the
`_ = f.Close()` idiom this repository writes deliberately. That form states that
the author ignored the error on purpose, and `check-blank` reads it as a defect.

**What the first CI run found.** One finding, from `staticcheck` and not from
`errcheck`: S1016 at `fs/shortwrite_test.go:178`. The local prediction covered
`errcheck` alone and reported zero, which is the more dangerous shape of a
partial instrument — a true zero that reads like a complete one.

### 3. The `tools` mutation baseline — DONE, PR #6 (`c9036fa`)

The mechanism already works. Only the baseline file and the CI step are absent.

- [ ] Run `scripts/mutation.sh --root tools --baseline tools/mutation-baseline.tsv`
      and read the score off the run.
- [ ] `tools/mutation-baseline.tsv`, floor taken from that run, survivor classes
      written in the form the `./crash/` row uses.
- [ ] The step added to the `tools` job.
- [ ] The note in `scripts/mutation-baseline.tsv` corrected — it describes the
      gap as open.

**Acceptance**

- The gate exits 0 against the new baseline and prints a measured score.
- The floor comes from a run, never chosen in advance. Last measured: 0.426866.
- `./scripts/mutation-selftest.sh` still passes.

---

## Group B — the external result

### 4. Sweep graphdb's positional paths

`Seek` and `WriteAt` shipped in PR #2. The graphdb paths that need them are
durability-critical and no sweep has reached them.

- [ ] The mmap snapshot publish, `pkg/storage/mmap_snapshot_writer.go:184`.
- [ ] The two page writes in `pkg/btree/pager.go`.
- [ ] The two `Seek` calls in `pkg/wal/wal.go`.

**Acceptance**

- The harness names **both** builds, as the last sweep did. A count against one
  build proves nothing — that discipline is what made 20/38 → 0/16 meaningful.
- Every finding carries a test that failed first, then a merged PR.
- A clean result carries a positive control beside it.

---

## Group C — the reach

### 5. The `fault/sql` design document

`sql.Register` and `driver.Driver` are a seam the standard library already
ships, and every Go service has the same shape. Six forks decide the package,
and none of them is a coin toss.

1. **`driver.ErrBadConn`.** `database/sql` retries twice
   (`maxBadConnRetries = 2`, `sql.go:1577`) and then opens a fresh connection.
   An injected error that satisfies `errors.Is(err, driver.ErrBadConn)` turns
   one armed point into several attempts, and the count stops meaning anything.
   Decide the error the adapter returns, and whether it refuses `ErrBadConn`.
2. **The pool.** The N-th operation is stable only under
   `SetMaxOpenConns(1)`. This is `fault/role`'s problem wearing new clothes.
   Decide between a documented limit, a runtime check, and a constructor that
   sets it.
3. **The optional interfaces.** `database/sql/driver` declares 28 interfaces.
   A wrapper implementing fewer than the driver beneath it silently changes the
   sequence, because `database/sql` falls back to prepare-then-exec. PR #2's
   problem, an order of magnitude larger.
4. **What counts as one operation.** If `Rows.Next` trips per row, a 600-row
   query exceeds `maxOps` (512) and the sweep reports non-termination — which
   reads exactly like a defect in the code under test, and is not one.
5. **The invariant and the leak count.** `sql.DB.Stats().OpenConnections` is
   the analogue of `fs.Outstanding()`. Decide whether the adapter keeps its own.
6. **The test driver.** The dependency gate forbids a real driver, so the tests
   need a `driver.Driver` written by hand inside the module.

**Acceptance**

- `docs/design/<date>-sql-adapter.md` exists.
- Each of the six forks has an answer and a reason.
- The limits section is as plain as `crash/doc.go`.

### 5b. `crash.Recorder.Observed()`

Raised by the graphdb session on 2026-08-30, with a live instance behind it.
Scheduled after task 5.

`pkg/search/lsa_persistence.go`'s `SaveToFile` calls `os.OpenFile` and
`os.Rename` directly, so no `FS` sees it and no recorder of ours can observe it.
Confirmed by reading the function, and counted with a control: `pkg/search` has
0 `.Sync()` calls, `pkg/storage` has 4.

The **total** bypass is already refused. `crash/run.go:54` returns
`errNoMutations` — "the scenario changed nothing, so there is no crash point and
the sweep proved nothing" — and the core has the same guard in `errNoOperations`.
A reader who believes that guard is missing will distrust one that works.

The **partial** bypass is the real gap and it is silent. A scenario that writes
some paths through the seam and others around it produces a non-empty record,
builds states, passes, and says nothing about what it never saw.
`errNoMutations` asks "did anything happen", not "did the thing you named
happen".

The precedent is `fs.Outstanding()` from PR #4: expose the fact, let the caller
assert. The library must not decide what "reached" means.

```go
if !slices.Contains(rec.Observed(), "index.lsa") {
    t.Fatal("the recorder never saw the LSA snapshot, so this sweep says nothing about it")
}
```

**Acceptance**

- `Observed()` returns the paths the recorder actually served.
- `crash/doc.go` states the limit: a path outside the seam is unobserved, and
  the total case fails while the partial case does not.
- A test covers the partial case — a scenario that bypasses the seam for one
  path and not another.

### 6–12. Implementation

Not writable until the design answers the forks. Probable shape, not decided:

- [ ] 6. The seam: `Driver`, `Connector`, `Conn`. Trip on connect, prepare, close.
- [ ] 7. Statements and transactions.
- [ ] 8. Rows, and the answer to the `maxOps` question.
- [ ] 9. Optional-interface forwarding, with a test proving the sequence does
      not move when a wrapped driver implements more than the wrapper.
- [ ] 10. The hand-written test driver, and a recorded-sequence test.
- [ ] 11. `doc.go` with the limits, and the `docs/couplings.tsv` rows.
- [ ] 12. The `fault/sql` baseline row, the README layout table, and `doc.go`.

Task 12 is not optional. `mutation.sh` exits 2 for a package the baseline omits,
so `fault/sql` cannot land without its row — the gate refuses rather than
reporting a smaller number, the same way the coupling registry did on the
`fs.Fault` rename.
