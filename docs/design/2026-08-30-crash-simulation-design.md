# crash simulation — design specification

- **Date**: 2026-08-30
- **Status**: approved for implementation
- **Module**: `github.com/dd0wney/fault`
- **Package**: `github.com/dd0wney/fault/crash`
- **Scope of this document**: the `crash` package only. It builds on
  `fault/fs` and on the durability rules stated here. It does not change the
  frozen core.

## 1. Purpose

The core package documentation names this gap in its own words:

> A sweep injects returned errors, not crashes.
>
> An error return is a cooperative failure: the caller is told, and it gets to
> unwind, clean up and report. A crash is not cooperative, and that difference
> is what makes the two find different defects.
>
> So a clean sweep does not verify a store's durability.

It then gives the worked example. A store that writes straight to its
destination file, instead of writing to a temporary one and renaming it into
place, is unsafe. A sweep does not say so, because after a failed write the
store's own cleanup deletes the partial file, so no bad state survives to be
observed. The same store loses data on a power cut between the write and the
sync.

`crash` closes that gap. SQLite runs three loops: out-of-memory, I/O error, and
crash. `fault` and `fault/alloc` are the first two. This is the third.

### 1.1 Why an error return cannot stand in for a crash

Every defence a Go program has against a failed operation is cooperative. A
`defer` runs. A cleanup path runs. An error travels up and something handles it.
A crash runs none of them, and the state it leaves is whatever the storage layer
had already made durable — which is not the same as what the program had
already written.

The two loops therefore find disjoint defect classes, and neither substitutes
for the other. A store can pass every sweep in this module and still lose data
on the first power cut.

## 2. Scope

### In scope for v0

- The `crash` package: `Record`, `Recorder`, `Run`, `Model`, `Cover`,
  `Exhaustive`, `Prefixes`.
- A power-loss model with reordering, at a unit the caller chooses.
- The POSIX metadata rule, with a switch for platforms that cannot obey it.

### Explicit non-goals for v0

| Excluded | Reason |
|---|---|
| A seeded sample `Cover` | A clean run under a sample means "no defect in the states this seed picked". The package's doctrine is that a pass must mean something. `Exhaustive` carries a cap, and `Prefixes` is the deterministic escape from it. Adding a sample later is compatible; removing it is not. |
| Crash **during** recovery | The check reopens once. A store that damages its own state while repairing it, and then dies again, is out of reach. This is a real defect class. It is named here rather than hidden. |
| Crash combined with an injected error | Needs a record produced by a run that took its error branch, and one happy-path record cannot describe that. §4.2 keeps the door open at no cost. |
| A stopping recorder, by panic and recover | §4.1. It buys only the row above, and it costs a panic driven through the code under test plus a stability check of the kind `fault/role` needs. |
| Modelling one named filesystem's exact ordering | The model is an approximation with a name, in the sense of §3. A store that survives it may still fail on a device that behaves differently, and §10 says so. |

## 3. Prior art

**SQLite's crash simulation.** The third loop, beside the out-of-memory and
I/O-error loops that this module already carries. It models writes at sector
granularity and can damage the sector that was in flight.

**ALICE**, Pillai and others, OSDI 2014. It introduced the **Abstract
Persistence Model**: a named, explicit statement of which operations are atomic
and which may be reordered, used to enumerate the states a crash can legally
produce. §7 is one such model, and the vocabulary of "legal state" is theirs.
The paper's contribution that matters most here is the discipline itself — that
the model must be written down and named, because a crash tester without one is
a random file corrupter.

**`testing/slogtest`.** Its `Run` "runs each case in a subtest". That is the
shape §8 follows, and it is the reason a candidate state gets a name that
`go test -run` can match.

## 4. Mechanism

### 4.1 Record once, replay many

The scenario runs **once**, against a recorder that wraps a real filesystem and
serves every call for real. The record is then replayed, many times, into fresh
directories.

The alternative is to re-run the scenario once for each crash point and stop it
there. It was considered and rejected.

| | Record once | Re-run per point |
|---|---|---|
| Scenario runs | 1 | N |
| Rebuilds | N × M | N × M |
| Needs panic and recover | no | yes |
| Needs a stability check | no | yes |
| Crash after an injected error | no | yes |
| Scenario stays outside a callback | yes | no |

Re-running does not, on its own, produce power loss. The live directory still
holds the unsynced bytes, because the operating system serves them to any reader
on the same machine. That is process death, not power loss. The pending-write
record is needed either way, so re-running adds runs without removing anything.

The two are **equally true** for a pure crash sweep. Up to crash point *k*, the
happy-path run is exactly what the live process did, because a process that has
not yet died takes no other branch. After *k* the dead process does nothing, and
the rebuild ignores the rest of the record. One record therefore describes every
crash point.

### 4.2 The record keeps an entry for every call, not only for mutations

A read changes nothing and carries no crash point. It still gets an entry, so
that an entry's index is the same operation number `fault.Sweep` would give it.

This costs almost nothing and it keeps one thing reachable: a later version that
records a run which took an injected error branch, and then walks crash points
through it. That is the union of the I/O-error loop and the crash loop, and it
is where the sharpest defects live. Aligned indexes are the whole of what v0
owes that future.

## 5. The durability model

At the moment of the crash, which changes are on the disk? The model keeps two
pending sets.

- **File data.** Every `Write` and every `Truncate` on an open file enters that
  file's pending set. A `Sync` on that file's handle clears it.
- **Directory metadata.** Every create, `Rename`, `Remove` and `MkdirAll` enters
  the pending set of the directory that holds the name. A `Sync` on a handle
  opened on that directory clears it.

`Close` clears nothing. POSIX does not make `close(2)` flush, and a store that
treats it as a flush holds the exact defect this package exists to find.

`fs.FS` needs no new method for the directory rule. `OpenFile(dir,
os.O_RDONLY, 0)` already returns a handle whose `Sync` the recorder sees, which
is also how a Go program syncs a directory in production.

A crash point sits after each operation that changes state. A read and a plain
open change nothing, so neither carries a crash point.

### 5.1 Windows cannot obey the POSIX rule

`Sync` on a directory handle fails on Windows, so a store that obeys POSIX
cannot obey POSIX there. `Model.MetadataDurable` makes a create, rename, remove
or mkdir durable as soon as it returns.

The strict rule is the default on purpose. A false alarm costs a reader an hour.
A missed defect costs data.

## 6. The record

The recorder wraps a real base and serves every call for real. It appends one
entry for each call.

```go
type entry struct {
    n      int    // index in the full operation sequence
    op     string // "create", "write", "truncate", "rename", "remove", "mkdir", "sync"
    path   string
    off    int64  // the file offset at the time of the call
    data   []byte // only the bytes that actually landed
    needs  []int  // indexes this entry depends on
}
```

`data` holds what landed, not what was offered. A real short write from the base
records its own count. The recorder reports the world truthfully, in the same
way `fs.Write` lets a real error outrank an injected one.

### 6.1 `needs`, and why a state without it is noise

`needs` decides whether a candidate state is **legal**. A crash cannot produce a
file that holds data and has no directory entry, so the generator must not
produce one either. The rule is a closure: if a state drops an entry, it drops
every entry that names it in `needs`.

- A write needs the create of the file it writes into.
- A rename needs the create of the name it moves.
- Two writes that overlap keep their log order, which replay gives at no cost.

A file that already existed when `Record` was called has no create entry, so
nothing depends on one. It arrives from the §6.2 snapshot and is durable by
construction. That is the mechanism which lets a test build its initial state
before recording.

Without the closure the package reports failures against states that no power cut
can make. Those are not findings. They train a reader to ignore the tool, which
is worse than having no tool.

### 6.2 The initial snapshot

`Record` copies the tree under `root` once and holds it. The replay starts from
a copy of that snapshot.

It earns its cost twice. §9.2's control needs it, and it lets a test build its
initial state **before** recording, so setup is durable by construction and
never enters the crash space.

### 6.3 The byte budget

The record holds bytes. Passing the budget fails the test and names the budget,
in the manner of `maxOps`. A record that quietly dropped its tail would build
states that no crash can produce, and the sweep would still pass.

## 7. The state generator

A model turns one record and one crash point *k* into a set of directories.

1. **Split durable from pending.** An entry at index ≤ *k* is durable when a
   sync that covers it appears after it and at index ≤ *k*, under the rules of
   §5. Everything else at ≤ *k* is pending. Entry *k+1* never started.
2. **Split the pending entries into loss units.** A write splits at sector
   boundaries of its absolute offset range. A metadata entry never splits,
   because no filesystem half-renames a file. A durable entry is not a unit — it
   is always present.
3. **Choose the subsets of the units**, by `Cover`.
4. **Close, remove duplicates, rebuild.** Apply the §6.1 closure. Two subsets
   often close to the same set, so deduplicate on the closed set before building
   anything. Then replay durable plus chosen, in log order, into an empty
   directory.

### 7.1 `Model`

```go
// Model says what a crash can lose.
type Model struct {
    // Sector is the unit a pending write can lose, in bytes. Zero loses a
    // whole Write call.
    Sector int

    // SplitAtCrashOnly limits sector splitting to the write at the crash
    // point. Every earlier pending write is then lost whole. This is what
    // SQLite's crash simulation does, and it is the cheapest way to keep the
    // part-written record without paying for it at every earlier write.
    SplitAtCrashOnly bool

    // MetadataDurable makes a create, rename, remove or mkdir durable as soon
    // as it returns. Set it for a store that must also run on Windows.
    MetadataDurable bool

    Cover Cover
}

// Cover chooses which candidate states the sweep visits.
type Cover int

const (
    // Exhaustive visits every legal subset of the pending units. It carries a
    // cap, and passing the cap fails the test.
    Exhaustive Cover = iota

    // Prefixes visits the states in which the pending units persist in log
    // order, so a state is a prefix. It costs u+1 states rather than 2^u.
    Prefixes
)
```

**The zero `Model` is the strict one.** `Sector: 0` loses a whole write,
`MetadataDurable: false` keeps the POSIX rule, and `Exhaustive` is the zero
value of `Cover` because it is first in the `iota` block. A caller who writes
`crash.Model{}` therefore gets the most demanding model, not the emptiest one.
This follows the core's care with `Points`, whose zero value arms nothing
because the counting happens before the comparison.

Two fields express all four loss units:

| Loss unit | `Sector` | `SplitAtCrashOnly` |
|---|---|---|
| One `Write` call | 0 | — |
| One sector, 4096 | 4096 | false |
| One sector, fixed 512 | 512 | false |
| Call, plus one part write | 4096 | true |

### 7.2 The state cap

`Exhaustive` visits 2^u states for *u* pending units, so it carries a cap.
Passing the cap fails the test, names *u* and the resulting count, and names
`Prefixes` as the way forward.

It must not quietly visit a subset. A partial walk reports a pass the same way a
complete one does.

## 8. Public API

```go
package crash // import "github.com/dd0wney/fault/crash"

// Record wraps base and records every change under root, so a crash state can
// be rebuilt. A name outside root cannot be rebuilt, and Run reports it rather
// than leaving it out of the states in silence.
func Record(base fs.FS, root string) *Recorder

// Recorder serves every call for real and records what changed. It is an fs.FS.
type Recorder struct{ /* unexported fields */ }

// Run rebuilds every state the model allows and runs check on each, one subtest
// for each state.
func Run(t *testing.T, rec *Recorder, m Model, check func(*testing.T, fs.FS))
```

Seven exported names: `Record`, `Recorder`, `Run`, plus `Model`, `Cover`,
`Exhaustive` and `Prefixes` from §7.1.

Counted the way the core specification counts, which is top-level exported
identifiers plus methods on concrete exported types — that is how `fault` gets
three from `Sweep`, `Points` and `Points.Trip`. Measured with `go doc`, not
remembered:

| Package | Names | Which |
|---|---|---|
| `fault` | 3 | `Sweep`, `Points`, `Points.Trip` |
| `fault/fs` | 5 | `FS`, `File`, `New`, `NewShortWrite`, `OS` |
| `fault/role` | 7 | `Sweep`, `Key`, `Key.String`, `Points`, `Points.Op`, `Role`, `Unknown` |
| `fault/crash` | 7 | the list above |

So `crash` matches `role` rather than exceeding it. A method on an interface is
not counted separately, because the interface's method set is its contract
rather than a second name.

`Run` is the only function that takes `*testing.T`, which keeps the core spec's
§4.1 rule: all three helpers in the standard library's `testing` tree take
`*testing.T` and not `testing.TB`.

The rebuilt `fs.FS` accepts the **original** names. It maps `root` onto a fresh
`t.TempDir()`, so the check opens the store with the same path constants the
scenario wrote with, and the check reads like the run. `t.TempDir()` also
removes the directory, which is again the core structure rather than a new one.

`Run` does not call `t.Parallel`. The states are independent, so the caller may
call it inside `check`. That is the caller's decision.

### 8.1 The shape at a call site

```go
rec := crash.Record(faultfs.OS(), dir)
if err := store.Save(rec, "v2"); err != nil {
    t.Fatal(err)
}

crash.Run(t, rec, crash.Model{Sector: 4096}, func(t *testing.T, fsys faultfs.FS) {
    s, err := store.Open(fsys)
    if err != nil {
        t.Fatalf("did not reopen: %v", err)
    }
    if v := s.Get(); v != "v1" && v != "v2" {
        t.Errorf("value is neither: %q", v)
    }
})
```

### 8.2 Naming a state

`role.Key` exists because an ordinal means nothing outside the run that produced
it. A subtest name has the same duty and a harder one, because `go test -run`
must match it later. Two levels, which `t.Run` nests for free:

```
TestCrash/after=data.tmp:write2/lost=data.tmp:write3+cfg:rename1
TestCrash/after=data.tmp:write2/lost=none
```

- A unit is named structurally: the file, the operation, and which occurrence of
  that operation on that file. A sector carries a suffix, `data.tmp:write3.s2`.
- `:` separates, because `/` would create a third level of nesting.
- When the lost set would make the name longer than a fixed limit, the name
  becomes `lost=6units:9f3a1c2b` — a count and a stable hash of the structural
  keys — and the failure message names every unit in full. A name a person
  cannot read is still a name `-run` can match, and the detail belongs in the
  message either way.

## 9. Failure modes and controls

The core has two diagnostics because a sweep that proved nothing reports the
same way as one that proved everything. A crash sweep needs more of them.

### 9.1 Refusals

Each fails the test and says which one fired.

| Refusal | Why it must not pass |
|---|---|
| The scenario changed nothing | No mutating entry, so no crash point and no subtest. `Run` would return having asserted nothing, which reads exactly like a complete pass. This is `errNoOperations` wearing a different coat. |
| A name outside `root` | It cannot be rebuilt. Leaving it out in silence builds a directory no crash can produce, and the sweep still passes. |
| The record passed its byte budget | §6.3. |
| `Exhaustive` passed the state cap | §7.2. |

A test cannot observe `t.Fatal` on its own `*testing.T`, and `testing.TB`
carries an unexported method, so nothing outside the testing package can supply
a stand-in. The core solved this by splitting `walk` out of `Sweep` and
returning an error. Reuse that decision exactly: an internal function returns
the diagnostic, and `Run` turns it into `t.Fatal`. Without the split, the four
most important error paths in this package are unreachable from any test.

### 9.2 The positive control

Before any candidate state, `Run` replays the **whole** record and compares the
result with the directory the scenario actually wrote — every name, every size,
every byte. If it does not match, the replay is wrong, every state built after
it is a fiction, and every finding is noise. `Run` fails the test.

This is what §4.1's approach was chosen for. It converts the one weakness of an
operation log into an assertion that runs on every scenario any user ever
writes, rather than on the fixtures we happened to think of.

## 10. Limitations

Stated in the package documentation, in the manner of the existing limits.

- **Nothing about a crash during recovery.** The check reopens once. A store
  that damages its own state while repairing it, and then dies again, is out of
  reach.
- **Nothing about a filesystem whose reordering the model does not express.**
  The model is an approximation with a name. A store that survives it may still
  fail on a device that behaves differently.
- **Nothing the check does not assert.** A check that only opens the store
  proves that the store opens. This is the same trap the core documentation
  names for adapters: a method that calls `Trip` and ignores the answer keeps
  the pass count exactly right, so counting passes never proves anything.

## 11. Test plan

Numbered to match §13 of the core specification.

### 11.1 Four reference stores, two of which must fail

A sweep that never fails anything is indistinguishable from a sweep that works.

| Store | What it does | POSIX model | `MetadataDurable` |
|---|---|---|---|
| `safeStore` | write temp, sync file, rename, sync dir | passes | passes |
| `noFileSync` | write temp, rename, sync dir | **fails** | **fails** |
| `noDirSync` | write temp, sync file, rename | **fails** | passes |
| `inPlace` | write straight to the destination | **fails** | **fails** |

`noDirSync` earns its place twice over. It passes under one model and fails
under the other, so it is the only row that can catch a `MetadataDurable` field
that is read and then ignored. Without it, that field could do nothing at all
and the whole suite would stay green.

### 11.2 Every refusal path

Each of §9.1's four refusals needs a test that reaches it, through the internal
function that returns a diagnostic.

### 11.3 The control needs its own control

Break the replay on purpose and assert that §9.2 fires. A watcher that cannot
report the negative reports nothing.

### 11.4 Metamorphic properties

- Replaying the whole record equals the base directory, over every fixture.
- `Prefixes` states are a subset of `Exhaustive` states, for the same record and
  crash point.
- `Sector: 0` and a sector larger than every write produce identical state sets.
- Two subsets that close to the same set rebuild byte-identical directories.

### 11.5 Negative controls

- A scenario that performs no mutation is refused, not passed.
- The state count for a small hand-worked record equals a number written into
  the test. A generator that quietly stops producing states is otherwise
  invisible, because fewer subtests still all pass.

### 11.6 Determinism

Run the same record twice and assert the state names come out identical **and in
the same order**. Go randomises map iteration on purpose, so a map anywhere in
the generator would make `-run` names unreproducible between runs. That is a
live hazard, not a theoretical one.

### 11.7 Windows

The POSIX metadata rule cannot hold there, because a directory handle cannot be
synced. The `MetadataDurable: true` path runs everywhere. The POSIX path needs a
build-tagged skip with a stated reason, and CI decides whether the reason was
right — local gates do not cover Windows.

## 12. Open decisions

1. **The byte budget of §6.3 and the state cap of §7.2.** Both need a measured
   number. Run the generator over the four reference stores of §11.1 and choose
   from what they actually produce. Neither value goes into the code before that
   run.
2. **The name length limit of §8.2**, before a state name becomes a count and a
   hash. Same rule: measure the four reference stores first.
3. **`crash` does not import the core.** Record-once uses no `Points` and no
   `Trip`, so the package depends on `fault/fs` and on nothing else in the
   module. It still belongs here: it shares the doctrine, it completes SQLite's
   third loop beside the other two, and the union described in §4.2 will need
   the core. The observation is recorded because it is surprising, not because
   it is a problem.
4. **Whether the module's zero-dependency gate needs a new case.** `crash`
   imports `path/filepath`, `os`, `testing`, `crypto/sha256` and `sort` at most.
   All are standard library, so the existing gate should already cover it. That
   is a read of the gate, not a run of it.
