# `fault/sql` — design specification

- **Date**: 2026-08-31
- **Status**: approved for implementation, 2026-08-31. The six forks below
  are answered, and each answer is confirmed. Tasks 8 to 14 of
  `docs/plans/2026-08-31-next-steps.md` are unblocked.
- **Module**: `github.com/dd0wney/fault`
- **Package**: `github.com/dd0wney/fault/sql`
- **Scope**: the adapter only. It needs `Points` and `Trip` from the frozen
  core and adds nothing to it.

Every measurement below was read from the Go source on this machine, at
go1.27.0, with the file and line recorded. A figure without a line number is a
figure nobody can re-check.

## 1. Purpose

`sql.Register` and `driver.Driver` are a seam the standard library already
ships. Every Go service that speaks to a database has the same shape, so an
adapter here reaches more code than any other adapter this module could add.

The core documentation's own claim is what the adapter must earn: capability
arrives through adapters, which need only `Points` and `Trip` and can live in
any module. `fault/sql` is the test of that claim against an interface this
module did not design.

## 2. What makes this harder than `fault/fs`

`fs.FS` has six methods and `fs.File` has five, and this module wrote both.
`database/sql/driver` declares **30 exported interfaces**, 28 of them in
`driver.go` alone, and `database/sql` decides at run time which of them a
connection implements. The adapter does not sit between a caller and a
filesystem. It sits between `database/sql` and a driver, and `database/sql`
changes its own behaviour based on what it finds.

That is PR #2's problem — a wrapper that implements fewer methods than the
thing it wraps — an order of magnitude larger, and with the fallback written
into the standard library rather than into a caller.

---

## 3. The six forks

### Fork 1 — `driver.ErrBadConn`

**Measured.** `database/sql` retries a bad connection. `sql.go:1577` sets
`maxBadConnRetries = 2`, and `DB.retry` at `sql.go:1579` runs:

```go
for i := int64(0); i < maxBadConnRetries; i++ {
	err := fn(cachedOrNewConn)
	if err == nil || !errors.Is(err, driver.ErrBadConn) {
		return err
	}
}
return fn(alwaysNewConn)
```

So one caller-visible operation becomes **up to three** driver calls, not two.
The loop body runs twice and the final line runs once more.

`driver.go:152` states the driver's own contract:

> To prevent duplicate operations, ErrBadConn should NOT be returned if there's
> a possibility that the database server might have performed the operation.

**Why it matters here.** The core arms the N-th operation. If the injected
error satisfies `errors.Is(err, driver.ErrBadConn)`, `database/sql` swallows it
and calls again, so the operation the sweep armed is not the operation the
caller sees fail, and the count stops meaning anything. Worse, the retry
happens on a NEW connection, so the adapter's own per-connection state is gone
by the time the caller is told.

**Options.**

1. Return a plain error and document that the adapter never produces
   `ErrBadConn`. Simple. Loses the whole `ErrBadConn` defect class, which is a
   real one: a caller that assumes retries are transparent is exactly the
   caller a fault injector should break.
2. Return `ErrBadConn` when asked, and accept that one armed point produces
   several attempts.
3. Refuse it: return a plain error by default, and make the sweep FAIL if an
   injected error is ever wrapped into an `ErrBadConn` by the code under test.

**DECIDED: option 1 for v0, with the refusal of option 3 added as a check.** The adapter returns an error that is deliberately not `ErrBadConn`,
and it asserts that fact rather than assuming it, because a driver beneath the
adapter could wrap it. The reason is the house rule that a false alarm costs a
reader an hour and a wrong count costs the meaning of every result: an
injection whose position the sweep cannot state is worse than an injection that
does not happen. `ErrBadConn` injection is a v1 feature with its own model of
the retry, not a v0 accident.

### Fork 2 — the connection pool

**Measured.** `sql.DB` is a pool. The N-th operation is stable only under
`SetMaxOpenConns(1)`. With more, two goroutines interleave their operations
through the same `Points` and the index the sweep armed belongs to whichever
one got there first.

This is `fault/role`'s problem wearing new clothes. `role` exists because a
global operation count is meaningless when several actors share it.

**Options.**

1. Document the limit and trust the caller. Cheapest, and it is the shape of
   defect this module exists to prevent: a limit stated in prose, obeyed by the
   examples, and broken silently by the first real user.
2. Check at run time. The adapter reads `db.Stats().MaxOpenConnections` and
   refuses when it is not 1. Honest, but it can only check after the caller has
   a `*sql.DB`, so the refusal arrives late.
3. A constructor that owns the setting: the adapter hands back the `*sql.DB`
   with `SetMaxOpenConns(1)` already applied.

**DECIDED: option 3, with option 2 as the guard.** The constructor sets
it, and the adapter still checks, because a caller can call
`SetMaxOpenConns(10)` afterwards and nothing would say so. Setting it without
checking is the "narrowing instrument" shape again: the adapter would be right
about the pool it created and silent about the pool in use.

A caller who genuinely wants concurrency wants `fault/role`, and the document
should say so rather than pretend the single-connection rule is a limitation
of the idea.

### Fork 3 — the optional interfaces

**Measured.** `sql.go:1777`:

```go
queryerCtx, ok := dc.ci.(driver.QueryerContext)
var queryer driver.Queryer
if !ok {
	queryer, ok = dc.ci.(driver.Queryer)
}
if ok { ... }
// otherwise: Prepare, then Query, then Close
```

`sql.go:1705` does the same for `ExecerContext` and `Execer`. So a wrapper that
implements neither turns **one** driver call into **three**. `driver.ErrSkip`
(`driver.go:150`) reaches the same fallback from inside a method that does
exist.

**Why it matters here.** The sweep's operation index is the whole product. If
wrapping a driver changes one call into three, then the same scenario has a
different number of operations with and without the adapter, and every armed
point moves. The adapter would be measuring itself.

**Options.**

1. Implement all 30. Correct, large, and every one is a place to get it wrong.
2. Implement the small set and document the rest. This silently moves every
   armed point for any driver that implements more than the wrapper.
3. Implement the set that changes the call count, and **assert** the rest: a
   test that fails when the wrapped driver implements an interface the wrapper
   does not.

**DECIDED: option 3.** The set that changes the call count is the set
that matters — `Execer`/`ExecerContext`, `Queryer`/`QueryerContext`,
`ConnPrepareContext`, `ConnBeginTx`, `SessionResetter`, `Validator`,
`NamedValueChecker`, `Pinger`. The rest can arrive later without moving an
index.

The assertion is the part that must not be skipped, and it is the direct lesson
of PR #2: a method set tracked by hand in two places drifts, and the drift is
invisible because everything still compiles. A test that reflects over the
wrapped driver and over the wrapper, and fails on a difference, is the only
form of this that stays true.

### Fork 4 — what counts as one operation

**Measured.** `sql.go:3086` calls `rs.rowsi.Next(rs.lastcols)` once per row. The
core's `maxOps` is 512 (`sweep.go:13`), and passing it returns
`errNoTermination` (`sweep.go:86`).

So if `Rows.Next` trips per row, a 600-row query exceeds `maxOps` and the sweep
reports non-termination — which reads exactly like a defect in the code under
test, and is not one.

**Options.**

1. `Rows.Next` trips per row. Honest about where a real failure occurs, and it
   makes any query over about 500 rows unusable.
2. `Rows.Next` never trips. The row-iteration error path is then untestable,
   and that path is a real defect class: a caller that ignores `rows.Err()`.
3. `Rows.Next` trips once per `Rows`, on a row the caller chooses.

**DECIDED: option 3.** One trip per result set, at a row index the
caller names, keeps the operation count proportional to the program's structure
rather than to its data. It also keeps the count STABLE when the fixture data
changes, which option 1 does not: adding a row to a test table would silently
move every later armed point.

The limits section must state plainly that a failure at row 400 of 600 is not
reachable unless the caller asks for it.

### Fork 5 — the invariant and the leak count

**Measured.** `sql.go:1207` — `DBStats.OpenConnections` is "the number of
established connections both in use and idle". It is the analogue of
`fs.Outstanding()` from PR #4.

**Options.**

1. Use `DBStats.OpenConnections`. No new code, and it counts what the POOL
   holds, not what the adapter handed out. A connection the pool is holding
   idle is not leaked.
2. The adapter keeps its own count of connections it created and did not see
   closed.

**DECIDED: option 2, and report both.** The two numbers answer different
questions, and the difference between them is itself the finding. This is the
same shape as `Recorder.Observed()` landed on this week: expose the fact and
let the caller assert, because the library must not decide what "leaked" means
for someone else's pool configuration.

### Fork 6 — the test driver

**Measured.** The dependency gate forbids a real driver, and as of 2026-08-31
it reads test imports too, so a driver imported from a `_test.go` file is
caught. That was closed deliberately before this package, not by accident.

**DECIDED.** A `driver.Driver` written by hand inside the module, in
`sql/internal/...` or behind an export test file. It must implement MORE
interfaces than the adapter does, so that fork 3's assertion has something to
detect. A test driver that implements exactly the wrapper's set would make that
test pass for free, which is the "testing a target that resembles the gate's
target" defect the mutation script already warns about.

---

## 4. Limits, stated as plainly as `crash/doc.go`

`sql/doc.go` carries this text. On the confirmed answers it reads:

- Nothing about `driver.ErrBadConn` retries. The adapter refuses to produce
  one, and asserts it did not.
- Nothing under a pool wider than one connection. Use `fault/role`.
- Nothing about a failure at an arbitrary row of a large result set.
- Nothing about an interface the wrapper does not implement, except that a test
  fails when the wrapped driver implements one the wrapper does not.

## 5. What lands before any code

Task 14 of the plan is not optional. `mutation.sh` exits 2 for a package the
baseline omits, so `fault/sql` cannot land without its baseline row. The
coupling registry needs its rows for the same reason.
