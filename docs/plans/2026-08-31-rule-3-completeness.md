# The rule 3 completeness check — 2026-08-31

## Why

The core's contract names rule 3 as the one that breaks silently:

> An operation that skips the call is invisible to the sweep, and the sweep then
> reports a clean walk over a sequence it never visited. Nothing detects this
> except a test that counts operations independently and compares.

**Measured today, and it is not theoretical.** A seventh method added to
`fs.FS`, implemented on `Fault` without calling `Trip`, passes:

| Gate | Result with the violation in place |
|---|---|
| 242 tests, six packages | all `ok` |
| `go vet` | exit 0 |
| coupling gate | 98.4%, exit 0 |
| mutation gate on `./fs/` | **1.000000, meets its 1.00 floor** |

The mutation gate cannot express it: `return f.base.Chmod(name, mode)` has no
mutant meaning "this should have called `Trip`".

The compiler is a real gate and it stops one thing only. Adding a method to
`fs.FS` breaks `crash`'s `remapFS` and `Recorder` and five test stubs, so a
method cannot be added without every implementation being written. Nothing
makes any of those implementations *count*.

`TestEveryOperationCallsTrip` is the countermeasure the contract names, and it
drives a hand-written table. The tables are complete today — 6 of 6 `fs.FS`
methods, 5 of 5 `fs.File` — so the library is correct now. Nothing keeps them
so.

## The shape

For each adapter, reflect over the interfaces it implements and partition every
method into two sets:

- **counted** — read from the SAME table the behavioural test already drives,
  never a second list. Two lists drift, and the drift is the defect.
- **exempt** — a method that deliberately does not count, with its reason as
  the map value.

A method in neither fails, naming it. A method in both fails. A name in the
table that is not in the interface fails, which catches a stale entry for a
method that was removed.

This is the shape `mutation.sh` already uses for packages ("these packages are
in the module and not in the baseline") and `fault/sql`'s reflective test uses
for interfaces. Both are in this repository. Neither is new.

**Duplication over a shared helper.** Each adapter's tests are an external test
package, so a shared helper needs an importable home, and the helper would then
be a dependency of the thing it checks. About fifteen lines repeated four times
is cheaper than that, and each copy states its own adapter's exemptions where a
reader of that package will find them.

---

## 1. `fault/fs`, the FS level

The reference adapter, and the one the hole was demonstrated in.

- [ ] Reflect over `faultfs.FS`. Every method must be a key of the existing
      `one` table, and every key of `one` must be a method.
- [ ] No exemptions at this level: all six change or read the filesystem.

**Acceptance**

- Adding a method to `fs.FS` and omitting the table entry FAILS, naming the
  method. Demonstrated by doing it, not asserted.
- Removing a method while leaving its table entry FAILS.
- The existing tests are untouched: the check reads `one`, it does not restate
  it.

## 2. `fault/fs`, the File level

- [ ] The same over `faultfs.File`, against the file-operation table at
      `fs/fault_test.go:258`.
- [ ] **State the limit in the test.** `Seek` and `WriteAt` are OPTIONAL and are
      not members of `fs.File`, so reflection cannot see them and their table
      in `positional_test.go` stays hand-maintained. A check that silently
      omitted them would be the same defect one level down.

**Acceptance**

- Adding a method to `fs.File` without a table entry FAILS, demonstrated.
- The comment names the optional pair and why they are outside the check.

## 3. `fault/alloc`

Two methods, and one of them is the first real exemption.

- [ ] Reflect over `alloc.Allocator`: `Bytes` counts.
- [ ] `Free` is exempt, and the reason goes in the map: it returns nothing, so
      it cannot report an injected failure to anybody. Verified in the code —
      `Fault.Free` calls the base and decrements, and never calls `Trip`.

**Acceptance**

- The exemption carries its reason as data, not as a comment beside the list.
- Removing `Free` from the exempt map FAILS, so the exemption cannot rot into
  an accident.
- Adding a method to `Allocator` without a decision FAILS.

## 4. `fault/sql`

The largest surface and the most exemptions, and the exemptions are the value:
`sql/doc.go` states them in prose today, and prose is not checked.

- [ ] `conn`, `stmt`, `rows`, `tx` and `Fault.Connect`.
- [ ] Exempt, each with the reason `doc.go` already gives: `NumInput` and
      `Columns` describe shape rather than performing work; the five
      `ColumnType*` methods and `HasNextResultSet` likewise; `ResetSession`
      because `sql.go:1353` discards any error that is not `ErrBadConn`, so
      there is no caller-visible failure to inject.
- [ ] `NextResultSet` counts, and is NOT exempt. It is the one member of that
      group that does work.

**Acceptance**

- Every exemption's reason in the map matches the reason in `doc.go`. If they
  disagree, one of them is wrong and the disagreement is the finding.
- Moving `NextResultSet` into the exempt map FAILS, because a test asserts it
  counts.
- Adding a method to any wrapped interface without a decision FAILS.

## 5. `fault/crash`

The recorder's rule is not rule 3, and the check has to say so. Every call takes
an index INCLUDING a read, because the index must equal the one
`fault.Sweep` would give the same operation.

- [ ] Reflect over `faultfs.FS` and `faultfs.File` against the `Recorder` and
      its `file`. Every method must record an entry.
- [ ] `Seek` and `WriteAt` again sit outside the interface and stay
      hand-maintained, with the limit stated.

**Acceptance**

- Adding a method that records nothing FAILS.
- `./crash/` is re-measured afterwards. It sits at 0.77 with 0.0098 of margin,
  and these are tests rather than production code, so the score can only rise
  or hold — but the recorded figure is re-read either way, because an identical
  figure about a larger suite is a different claim.

## Not in scope, and why

**`fault/role`.** It is not an adapter wrapping an interface. `Points.Op` is
the counting primitive itself, so there is no method set to compare a table
against. Its correctness question is a different one — whether an actor's own
sequence is stable — and `role`'s stability check already answers it.

**Rule 4.** The contract says outright that it has no automatic check, because
no assertion can know what the real resource would have returned. Nothing here
changes that, and pretending otherwise would be worse than leaving it named.

## Sequence

**1 → 2 → 3 → 4 → 5.**

`fs` first because it is the reference adapter and the hole was demonstrated
there. `alloc` next because it is two methods and introduces the exemption
shape on the smallest possible surface. `sql` fourth because it has the most
exemptions and they are worth writing once the shape is settled. `crash` last
because its rule differs and the check needs a different sentence.

Each task is one commit, and each commit's acceptance includes DEMONSTRATING
the failure rather than asserting it. A completeness check that has never been
watched to fail is the same shape as the gap it closes.
