# fault core — design specification

- **Date**: 2026-08-29
- **Status**: approved for implementation
- **Module**: `github.com/dd0wney/fault`
- **Scope of this document**: the root `fault` package only. The `fs` and
  `alloc` adapters are specified far enough to fix the contract they depend on;
  each gets its own document.

## 1. Purpose

`fault` walks the point of failure through every operation a test scenario
performs. It fails the first operation, then the second, then the third, and it
stops when the fault runs off the end of the sequence. Every error path the
scenario can reach has then been executed.

SQLite states the loop plainly:

> Rig the alternative interface to give an I/O error on the N-th system call,
> for N=1,2,3,.... Repeat until no I/O errors occur.

The termination condition carries the meaning. A run that completes without the
fault firing proves that N has passed the end of the sequence.

A single fault test picks one point in that sequence, and picks it by luck. The
interesting points sit inside a rename, between a write and its sync, and on the
close after a failed sync. Nobody guesses those.

### 1.1 The activity this automates has a name

DO-178C, the airborne software standard, splits requirements-based testing in
two. Normal range testing checks the response to expected inputs. Robustness
testing is the other half, and DO-178C defines it as:

> Robustness testing involves stressing the system, including comprehensively
> testing it with the full range of equivalent data classes, and testing its
> behavior when abnormal conditions and faults are present.

That second clause is this package. At every design assurance level except the
lowest, robustness tests are expected for every high-level and low-level
requirement.

Two consequences are worth stating, because they shape what the library is for.

The standard also defines **equivalence classes**: classes of data that behave
identically, of which only one member need be tested. A failure point is the
same kind of object. A structural key — actor, operation type, occurrence
within that actor — names an equivalence class over failure points, which is
why §11.1 treats the key as the durable artefact and a random seed as
disposable. Two seeds may land in the same class, and neither one names it.

None of this claims certification. No qualified Go compiler exists, so Go
software does not achieve the higher design assurance levels, and this document
must never imply otherwise. The measures transfer where the certification does
not, and they are the best-defined measures of test adequacy available.

## 2. Scope

### In scope for v0

- The `fault` package: `Sweep`, `Points`, and one method on `Points`.
- A filesystem adapter (`fault/fs`) and an allocator adapter (`fault/alloc`),
  each specified separately, both built only on the public contract in §8.

### Explicit non-goals for v0

| Excluded | Reason |
|---|---|
| Roles, structural keys, `Explore` — **in the `fault` package** | Concurrency machinery. It is the most valuable part of the source material and the least settled. Freezing it under a compatibility promise now is the wrong order. It ships in v0 as a sibling package, `fault/role`, outside the frozen core. See §11 and §11.1. |
| Crash and power-loss simulation | A large surface for one scenario. It belongs in an adapter, not in the core. |
| Convenience arming (`FailOpen`, `FailWrite`, `FailSync`) | A layer above the minimum. Adding it later is compatible; removing it is not. |
| A `FailAllFrom` mode in the core | §10 shows an adapter implements this on top of the core with no core change. Putting it in the core would double the core's state machine to serve one adapter. |
| Mutation testing, coverage floors, coupling coverage, contract digests | Build-time tooling. It lives in the separate `fault/tools` module, and it can never enter the standard library because it shells out to external binaries. |

## 3. What this code owes the test suite

Applying the level table from the `designing-for-testability` skill:

- The code is a library others build on, and a defect in it stays silent — a
  broken sweep reports a pass. That is the highest row: **level 4**.

Level 4 owes: happy path, every returned error, branch coverage, one test per
error path, fault injection, condition coverage, and metamorphic tests. §13 is
the test plan that discharges this.

The "a defect stays silent" property deserves emphasis. If `Sweep` terminates
one pass too early, every test built on it still passes, and the error paths it
skipped are never reported as unvisited. The library's own failure mode is a
false green. §13.4 exists for that reason alone.

## 4. Public API

The exported surface is frozen at three symbols. This is a deliberate budget,
taken from what the standard library actually ships: `testing/cryptotest`
exports one symbol, `testing/synctest` exports three.

```go
package fault // import "github.com/dd0wney/fault"

// Sweep yields the operation number to arm, and the Points that carries it.
func Sweep(t *testing.T) iter.Seq2[int, *Points]

// Points counts the operations a scenario performs and reports which one
// must fail.
type Points struct { /* unexported fields */ }

// Trip counts one operation and reports whether this is the operation that
// must fail.
func (p *Points) Trip() bool
```

### 4.1 Decisions behind the signatures

**`Trip` returns `bool`, not `error`.** A bool lets each adapter produce an
error its own domain would produce — `&os.PathError{Err: syscall.EIO}` for a
filesystem, a refusal for an allocator — instead of a single library sentinel
that no production caller would ever see. It also keeps a fourth exported symbol
out of the surface. The cost is two lines per adapter method rather than one.

**The name is `Trip`.** The method counts, so an adjective (`Armed`) would hide
a side effect. `Trip` is a verb, it carries the circuit-breaker sense of a point
that fires once, and it reads correctly at the call site, which is the only
place it ever appears:

```go
if p.Trip() {
    return nil, &os.PathError{Op: "open", Path: name, Err: syscall.EIO}
}
```

`Step` and `Next` were rejected: both suggest advancing a cursor and neither
suggests failure. `Fails` was rejected as a predicate name for a method with a
side effect.

**`Sweep` takes `*testing.T`, not `testing.TB`.** All three helpers in the
standard library's `testing` tree take `*testing.T`: `cryptotest.SetGlobalRandom`,
`synctest.Test` and `slogtest.Run`. `testing.TB` would allow a sweep inside a
benchmark, which is not a use case worth a divergence from the tree this package
hopes to join.

**`Sweep` takes no bound parameter.** The runaway bound is the constant in §6.3.
A parameter for it would appear in every call site to serve a case that means
the caller has a bug.

### 4.2 Prior art for `Trip`

SQLite ships the same function, and has for years:

```c
int sqlite3FaultSim(int eCode);

if( sqlite3FaultSim(200) ){
    rc = 1;
}else{
    rc = pthread_create(&p->tid, 0, xTask, pIn);
}
```

From D. Richard Hipp's talk of 2026-07-16: it returns 0 by default, its
non-zero returns are controlled through the public `sqlite3_test_control()`
API, and — the detail that matters here — "Optionally: only on the N-th call".
There were 24 instances in the SQLite source as of 2026-07-02.

This settles three things that were open:

1. **The return type.** A predicate that the caller turns into its own
   domain-appropriate error is the shape SQLite arrived at independently, after
   two decades of use. `Trip() bool` matches it.
2. **The call-site shape.** `if fault { fail } else { do the real thing }` is
   the adapter contract in §8, written in C.
3. **The seam ships.** `sqlite3_test_control()` is present in every production
   build. Hipp's slide gives the reason in four words: "Fly what you test."

One difference is deliberate and is an improvement, not a divergence. Hipp
describes SQLite's syscall override table as "Singleton. Not threadsafe.
Testing use only." A `Points` is none of those: it is per-sweep rather than
global, it is safe for concurrent use (§5), and nothing about it is hidden.
The cost of the singleton is that two sweeps cannot run at once, which in Go
would mean forbidding `t.Parallel`.

## 5. `Points` semantics

```go
type Points struct {
	mu    sync.Mutex
	armed int  // the operation that must fail, counting from 1; 0 arms nothing
	seen  int  // operations counted so far in this pass
	fired bool // whether the armed operation was reached
}
```

`Trip` must hold the following, exactly:

1. Every call increments `seen`.
2. It returns `true` on the call where `seen == armed`, and it sets `fired`.
3. It returns `false` on every other call, **including every call after the
   armed one**. A pass fails one operation and no more.
4. `armed == 0` never returns `true`. The zero `Points` is therefore usable and
   inert, which is what a caller gets if they construct one by hand.
5. It is safe for concurrent use. §11 states the limitation this does not remove.

A `Points` belongs to exactly one pass. `Sweep` constructs a fresh one for each
pass rather than resetting a shared one, so a `Points` that outlives its pass
(captured by a goroutine the scenario leaked) cannot corrupt the next pass.

### 5.1 Unexported accessor

`Sweep` must read `fired` under the mutex. That needs one unexported method:

```go
func (p *Points) hasFired() bool
```

No `arm` method is required. `Sweep` is in the same package, so it constructs
`&Points{armed: n}` directly.

## 6. `Sweep` semantics

### 6.1 The pass algorithm

For `n = 1, 2, 3, …`:

1. If `n` exceeds the bound, fail the test (§6.3, case B).
2. Construct `p := &Points{armed: n}`.
3. Yield `(n, p)`. If `yield` returns `false`, the caller used `break`; return
   with no diagnostic.
4. If `p.hasFired()`, continue to `n+1`.
5. Otherwise the fault ran off the end of the sequence:
   - if `n == 1`, fail the test (§6.3, case A),
   - otherwise return. The walk is complete.

For a scenario performing K operations, where K ≥ 1, this yields exactly K+1
passes: one that fails each operation in turn, plus the final pass that proves
the sequence ended. The final pass is not an off-by-one. Without it, a sweep
could stop early and still look correct.

### 6.2 What the loop body owes

The body runs the scenario and asserts the invariant. The library takes no
position on what a broken invariant costs:

- `t.Errorf` walks the whole sequence and reports every point that fails.
- `t.Fatalf` stops at the first one.

Both are correct, both already mean the right thing in Go, and the library
exports nothing to choose between them. This was verified against the compiler:
`t.Fatal` calls `runtime.Goexit`, and the range-over-func rewrite handles it the
same way it handles `break`, so deferred cleanup inside the iterator still runs.

### 6.3 Failure modes

Both are failures of the sweep itself, not of the code under test. Both call
`t.Fatal`, because a sweep that cannot prove what it claims must not report a
pass.

**Case A — the scenario performed no operations.**

```
fault: the scenario performed no operations, so the sweep proved nothing
```

This is the package's own negative control, and it is the single most valuable
line in it. A sweep over a scenario that does no I/O terminates immediately and
passes, which is indistinguishable from a sweep that visited every error path
and found nothing wrong. graphdb found four tests in this state on 2026-08-28:
they ran the code they claimed to test and asserted nothing useful about it.

**Case B — the walk did not terminate.**

```
fault: the sweep did not terminate within 512 operations: the scenario
performs more work on each pass, or the adapter does not call Trip
```

The bound is a package constant:

```go
const maxOps = 512
```

Exceeding it fails rather than passing quietly, because a sweep that never
reaches its end has not proved what it claims. The message names both causes,
because the two are indistinguishable from inside the loop and a reader hitting
this needs to know which to check.

## 7. Internal structure, and the seam

The two diagnostics in §6.3 call `t.Fatal`. A test cannot observe `t.Fatal` on
its own `*testing.T`, and `testing.TB` carries an unexported method, so no
package can supply a stand-in for it. Left as written, the two most important
error paths in the library are unreachable from any test.

Per the rule — *if a test cannot make a dependency fail, the design is wrong, not
the test* — the design changes. Split the walk from the reporting:

```go
// walk runs the sweep and returns a diagnostic instead of failing a test.
// It contains all of the logic in §6.1.
func walk(yield func(int, *Points) bool) error

var (
	errNoOperations  = errors.New("fault: the scenario performed no operations, so the sweep proved nothing")
	errNoTermination = errors.New("fault: the sweep did not terminate")
)

// Sweep is the thin wrapper that turns a diagnostic into a test failure.
func Sweep(t *testing.T) iter.Seq2[int, *Points] {
	return func(yield func(int, *Points) bool) {
		t.Helper()
		if err := walk(yield); err != nil {
			t.Fatal(err)
		}
	}
}
```

`errNoTermination` is wrapped with the bound and the two causes when returned,
so `errors.Is` works on the sentinel and the message stays specific.

An external test reaches `walk` through `export_test.go`, which is the standard
library's own mechanism for this:

```go
// export_test.go
package fault

var (
	Walk             = walk
	ErrNoOperations  = errNoOperations
	ErrNoTermination = errNoTermination
)
```

This ships nothing. `export_test.go` compiles only under `go test`, and it
re-exports the production function rather than substituting a second
implementation, so the tested artifact stays the shipped artifact.

`Sweep` itself keeps one test (§13.3) proving it forwards a diagnostic to
`t.Fatal`. That test uses a subtest and asserts the subtest failed.

## 8. The adapter contract

This is the load-bearing interface of the whole design, and it is prose rather
than a Go type on purpose: the core has no opinion about what an operation is.

An adapter **must**:

1. Call `Trip` exactly once, immediately before each operation it performs.
2. Skip the operation entirely when `Trip` returns `true`.
3. Return an error its own domain would produce for that operation.
4. Call `Trip` for every operation it performs, including ones it performs
   internally. An operation that skips the call is invisible to the sweep, and
   the sweep will report a clean walk over a sequence it did not visit.

An adapter **must not**:

1. Call `Trip` more than once for one operation. The count is the sequence, and
   a double count shifts every later point.
2. Retry an operation that `Trip` failed. Retrying inside the adapter hides the
   error path from the code under test, which is the thing being tested.

Rule 4 is the one that breaks in practice, and it breaks silently. §13.5 requires
each adapter to carry a test that counts operations independently and compares.

## 9. `fault/fs` — filesystem adapter (contract only)

Specified in full in a later document. Fixed here:

- Package `fs`, import path `github.com/dd0wney/fault/fs`. Callers who also
  import `io/fs` must alias. This is accepted: the import path reads
  `fault/fs`, which is self-describing, and the alternative names
  (`faultfs`, `diskfault`) are worse at the import line.
- Method names and signatures match `os.Root` (Go 1.24) wherever the two
  overlap. A future standard library proposal built on an accepted shape is
  easier to accept than one built from nothing.
- It wraps an existing filesystem rather than replacing it, so a test can point
  it at a real temporary directory.
- The standard library has no writable filesystem interface, and it is not
  getting one soon. Proposal `golang/go#45757`, "io/fs: add writable
  interfaces", is **closed** — frozen due to age, with no resolution. The Go
  team's position is that `io/fs` is read-only by design. This package
  therefore defines its own interface, and its odds of ever entering the
  standard library are poor. Design it for its users, not for a proposal.

## 10. `fault/alloc` — allocator adapter (contract only)

Specified in full in a later document. One property belongs here, because it
justifies a non-goal in §2:

SQLite runs two loops, not one. The first refuses the N-th allocation and then
behaves normally, which finds handlers that cope with a single failure. The
second refuses the N-th allocation **and every one after it**, which finds
cleanup paths that allocate in order to clean up.

The core provides the first. The adapter provides the second by remembering that
`Trip` returned `true` and refusing on its own from then on. No core change is
needed, and the mode stays in the one package that has a reason to care.

### 10.1 The overlay pattern

SQLite's `malloc.html` names the structure this adapter must copy:

> An application can insert layers or "overlays" in between the SQLite core and
> the underlying memory allocator. For example, the out-of-memory test logic for
> SQLite uses an overlay that can simulate memory allocation failures. ... The
> existing allocator is saved by the overlay and is used as a fallback to do
> real memory allocation.

An overlay wraps a real allocator rather than replacing it. Every allocation the
sweep does not refuse is served for real, so the code under test allocates real
memory and the sweep tests the real path. `fault/fs` follows the same rule: it
wraps a real filesystem.

### 10.2 The adapter must count leaks, not only failures

The fault-injection loop needs three assertions, and dropping any one makes it
decoration:

1. The operation must not succeed quietly.
2. Nothing must leak.
3. The state must remain valid.

Assertion 2 is the one a caller forgets, and it is the one an allocator adapter
can supply for free: it already sees every allocation and every release, so it
can report the outstanding count. SQLite does exactly this — every test script
destroys all objects at the end and checks that all memory was freed, which is
how it detects leaks at all.

`fault/alloc` therefore exports an outstanding-allocation count, and the loop
body asserts it returns to zero. Without it, every unwind path in the code under
test can leak and the sweep still reports a clean walk.

SQLite's instrumented allocator does four more things that are out of scope here
but belong in a later version, because Go's runtime does not provide them at an
interface boundary: sentinel bounds checking, poisoning memory on free,
detecting a free of memory that came from elsewhere, and poisoning on allocation
so a caller cannot assume zeroed bytes.

## 11. Limitations

**`Sweep` is for single-actor scenarios.** `Points` is safe for concurrent use,
which prevents a data race. It does not make the walk meaningful under
concurrency: with several goroutines, the N-th operation overall is a different
operation on every run, because the scheduler picks the interleaving. A sweep
over that visits an arbitrary subset of the error paths while appearing to visit
all of them — and it still terminates, and it still passes.

The fix is a per-actor counter and a structural key naming the actor, the
operation type and the occurrence within that actor. That is out of scope for
the `fault` package (§2), not out of scope for the release (§11.1). The package
documentation must state this limitation plainly, because a user who does not
know it gets a false green.

### 11.1 `fault/role` ships in v0, outside the core

Market research contradicted the original plan to defer this entirely, and the
evidence is strong enough to change it.

The structural key is the **emptiest gap in the Go ecosystem** of anything in
this project. No public Go library gives a per-actor failure identity. The
nearest attempt, `jellevandenhooff/gosim` (81 stars), rewrites the Go runtime
for scheduler simulation and still stops short of a named per-actor key; it has
had no commit since December 2024. Polar Signals' simulation testing reproduces
from a raw random seed, not from a name that survives a scheduler change.
Antithesis sells the idea as a paid platform, not as an importable library.

Deferring the one capability nobody else offers, in order to protect the
proposal odds of a capability whose odds are poor anyway (§12.1), is the wrong
trade. So:

- `fault` keeps its three frozen symbols. It remains the proposal candidate.
- `fault/role` ships in the same v0 release and carries `Role`, `Key`,
  `SweepRole` and `Explore`. It is explicitly **not** frozen, and it says so in
  its package documentation.
- `fault/role` depends on `fault`, never the reverse.

This is the same layering as the adapters. The core stays small because the
interesting work lives one package away, not because the interesting work was
cancelled.

## 12. Compatibility and proposal constraints

| Constraint | Value | Reason |
|---|---|---|
| Exported symbols in `fault` | 3 | The budget in §4. Every addition is permanent. |
| Dependencies | Standard library only, in every package except `fault/tools` | The standard library permits none. |
| `go` directive | `1.23.0` | `iter.Seq2` landed in Go 1.23. Nothing in the design needs more, and the directive is an adoption floor for every consumer. |
| `fault/tools` | Separate module, `go 1.24.0` | `tool` directives need 1.24. A separate module keeps the library's floor low. |
| Licence | BSD-3-Clause | Merged code takes the licence of the tree it joins. |
| Version | v0 until the adapter contracts settle | §8 is the interface most likely to need a change. |

An eventual proposal covers `fault` and, with much lower confidence, `fault/fs`.
It never covers `fault/tools`.

### 12.1 The proposal, stated honestly

The standard library goal shaped this design, and it was worth that. It forced
the smallest true abstraction, and the result — three exported symbols, zero
dependencies, adapters one package away — is a better library than the
alternative. Keep the discipline.

Do not, however, plan around the merge happening.

| Capability | Odds | Reason |
|---|---|---|
| I/O fault-injection sweep | Poor | Needs a writable filesystem interface first. Proposal 45757 is closed, and `io/fs` is read-only by design. |
| Allocator that refuses | Poor | Go exposes no user-level hook into its own allocator, so the standard library has no natural place for it. |
| Structural key | Fair, still low | `testing/synctest` proves the Go team will ship a deterministic concurrency tool. It solved scheduling and time, not fault-point identity, so this needs its own proposal with no direct precedent. |

Every testing subpackage in the standard library was written by the Go team. No
`testing/*` package began as an external module that a maintainer merged.

The mechanism is not a code merge. `slices` and `maps` were trialled as
`golang.org/x/exp/slices` and `/maps`, and what carried them into Go 1.21 was a
measured import count: 1,541 importers of `slices` and 615 of `maps` at proposal
time. The Go team then wrote a fresh version. `log/slog` took the same route.
The control case is `golang.org/x/sync/errgroup`: proposed for `sync` in
December 2022, in wide use for years, and still undecided.

**The path is import count, not argument.** Publish, get real projects using it,
and revisit in years. §14 tracks this.

## 13. Test plan

All tests live in package `fault_test` unless stated. An external test package
can only reach exported names, so it fails the moment the public API is wrong,
and it cannot quietly reach `hasFired` or `walk` except through the seam in §7.

### 13.1 Happy path

- A scenario performing K operations yields exactly the passes `1 … K+1`.
- The last pass returns no injected error.
- Pass `n` fails operation `n` and no other operation.

### 13.2 Every error path (§6.3)

Both through `Walk`, from §7:

- A scenario performing zero operations returns `ErrNoOperations`.
- A scenario performing one more operation on each pass returns
  `ErrNoTermination`.

### 13.3 The wrapper

- `Sweep` forwards a diagnostic to `t.Fatal`. Run the sweep inside a subtest and
  assert the subtest failed.

### 13.4 Metamorphic properties

These catch the false-green failure mode named in §3, which the example-based
tests above cannot. For every K in a range:

- The pass count is exactly K+1.
- The set of operation indices that failed across all passes is exactly
  `{1 … K}` — no gap, no repeat.
- Removing one operation from the scenario removes exactly one pass.

### 13.5 Negative controls

Required, per `verify-the-instrument`. Each of these must be seen to fail before
it is believed:

- The `{1 … K}` assertion must fail against a `Trip` that returns `true` twice.
- The pass-count assertion must fail against a `walk` that omits the final pass.
- `ErrNoOperations` must not fire for a scenario performing exactly one
  operation. This is the boundary that separates "proved nothing" from the
  smallest real sweep.

### 13.6 Concurrency

- `go test -race` with a scenario calling `Trip` from several goroutines. This
  proves the mutex, and nothing more. It must not be read as support for the
  case §11 excludes.

## 14. What this competes with

Two public Go libraries occupy the neighbouring space, and both are active:

| Library | Stars | How it works |
|---|---|---|
| `pingcap/failpoint` | 894 | A developer hand-places a named failpoint at each call site. A test arms it by name. |
| `etcd-io/gofail` | 425 | The same model: named failpoints, placed by hand, armed by name. |
| `cockroachdb/pebble` `errorfs` | — | Internal to Pebble. Not exported for outside use. |
| `spf13/afero` | 6,693 | Filesystem abstraction with an in-memory backend. No fault injection at all. |

The difference to state plainly in the README, because it is the whole pitch:

> A failpoint fails where you thought to put one. A sweep fails everywhere,
> including the places you did not think of, and it tells you when it has run
> out of places.

Three properties none of the above has: the walk is **automatic** (no hand-placed
sites), it is **exhaustive** (every operation, in turn), and it **terminates on
its own** and says so.

The audience is projects that already build this by hand: embedded key-value and
LSM stores, Raft implementations, distributed storage layers, message brokers
with a durability promise, and backup tools. SQLite, etcd, PingCAP and
CockroachDB each wrote a private version because no shared Go library existed.

## 15. Open decisions

1. **The GitHub repository must be named `fault`**, to match the module path.
   The local directory name does not matter.
2. **Adoption.** §12.1 sets the route: import count over years. The nearest
   model is `uber-go/goleak`, which was adopted after Uber published a measured
   result — 857 pre-existing goroutine leaks found across 75 million lines of
   Go — and then released the tool that found it. The equivalent measurement for
   this library is: run the sweep against a well-tested Go storage project and
   count the error paths that had never executed. That number is the launch, not
   the README.
3. **Whether `fault/role` lands in the first release or the second.** §11.1
   argues for the first. The cost is that v0 then carries an unfrozen package
   next to a frozen one, and the distinction must be unmissable in the docs.
