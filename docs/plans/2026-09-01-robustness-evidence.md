# Robustness evidence for a program that uses this library

Written 2026-09-01, against `main` at `9933712`. Every number below was measured
while writing this.

The goal is narrow and it is not the goal of the earlier plans. Those asked what
this repository should publish. This one asks what a **caller** can produce: a
program whose tests drive `fault.Sweep` should be able to say which of its own
component interfaces have had their error paths executed, and which have not.

---

## The finding this plan exists for

`sweep.go:80` states the shape of a walk:

> For a scenario performing K operations, where K >= 1, this yields exactly
> K+1 passes: one that fails each operation in turn, plus the final pass that
> proves the sequence ended.

DO-178C 6.4.2.2 splits requirements-based testing in two, and
`docs/design/2026-08-29-fault-core.md:31` already quotes the definition: normal
range testing checks the response to expected inputs, and robustness testing is
the other half.

**A sweep already produces both categories, in one loop.** Passes 1..K are
robustness cases — each one executes the scenario with one operation failing.
The terminating pass is the normal range case: nothing fires, and the scenario
runs clean.

Nothing separates them afterwards. `go test -coverprofile` accumulates counters
across the whole binary, so the profile that `go tool coupling` reads is the
union of K robustness passes and one normal range pass. A coupling site touched
**only** by the terminating pass therefore reports as covered, and its error
path has never executed.

That is this repository's own defect class, moved one level out. Inside a
component, the equivalent is a statement counted as covered by a test that
asserts nothing. At the interface, it is a coupling counted as exercised by a
run in which nothing failed. The registry cannot see the difference, the tool
cannot see the difference, and the number that comes out is honest about the
wrong question.

### Why this is worth building rather than documenting

DO-178C asks for the two things separately — robustness test cases (6.4.2.2) and
data/control coupling coverage (6.4.4.d) — and an assessor reads them together.
The pairing has no mechanical expression in any language the author is aware of,
and Go has neither piece publicly. `docs/design/2026-08-29-coupling-coverage.md`
§9 records that coupling coverage alone had no public Go equivalent. This is
that measure, restricted to the runs where something actually failed.

### The parts already exist

| Part | Where | Verified |
|---|---|---|
| Per-pass counter capture | `runtime/coverage.ClearCounters`, `WriteCountersDir` | `go doc runtime/coverage`, go1.27.0 |
| Set arithmetic over profiles | `go tool covdata subtract`, `intersect`, `merge` | `go tool covdata` subcommand list |
| Conversion to the text profile | `go tool covdata textfmt` | same |
| The measure itself | `go tool coupling` | built, tested, run in CI every commit |

Every one is in the standard toolchain or the standard library. The dependency
gate stays exactly as strict as it is: 158 packages in the root module, standard
library only.

---

## Measured state, 2026-09-01

| Item | Measured |
|---|---|
| `main` | `9933712`, read from `git log`. PRs #8–#28 merged. |
| Coupling registry | 14 rows. `go tool coupling` runs in CI on every commit. |
| `tools` module tags | **None.** `git ls-remote --tags origin` returns `v0.1.0` only, which serves the root module. |
| `tools/go.mod` | Declares `tool github.com/dd0wney/fault/tools/coupling`. Nobody outside this repository can resolve it. |
| `runtime/coverage.ClearCounters` | Errors without `-cover`, **and** without atomic counter mode. Both must be refusals, not warnings. |
| `alloc.Fault` | `Bytes`, `Free`, `Outstanding`. No `MaxOutstanding`. |
| `sql.Fault` | `Connect`, `Driver`, `Err`, `Outstanding`. No `MaxOutstanding`. |

### Two things checked and found not to be defects

Recorded because a plan that lists only its hits is not a record of what was
examined.

`alloc.Fault.Free` decrements `outstanding` with no guard, which reads like a
defect. It is not: `alloc/fault.go:83` argues that a negative count is the only
thing that reports a deferred `Free` placed before the error check, and that
clamping at zero "would turn that finding into a plausible zero, and a plausible
zero is what a leak detector must never produce".
`TestAnUnmatchedFreeDrivesTheCountNegative` is the gate.

A directory sync appeared to be unreachable through `fs.FS`, which has no method
for it. It is reachable: `crash/doc.go:67` records that
`OpenFile(dir, os.O_RDONLY, 0)` returns a handle whose `Sync` the recorder sees,
and `crash/stores_dirsync_test.go` drives it.

---

## Group A — unblock the measurements

### 1. Merge #28, the working-tree guard  ✔ done 2026-09-01

The mutation tool edits in place, so a run killed partway leaves the tree
mutated and the next run there scores 1.000 against a suite that is already
failing. Every task below re-measures a mutation floor, so the guard has to be
in first.

**Acceptance** — `gh pr view 28` reports MERGED; CI green on the merge commit;
`scripts/mutation-selftest.sh` clean in a fresh worktree.

### 2. Merge #27, the runnable examples  ✔ done 2026-09-01

118 lines of example code live in doc comments and nothing compiles them. A
documented call that cannot compile is a defect in the API contract, and
`ad45eea` is the proof that it has happened here already.

**Acceptance** — `gh pr view 27` reports MERGED; `go test ./...` executes the
five `Output` blocks.

---

## Group B — the evidence has to be sound before anyone produces it

This group is first on purpose. An evidence artefact built over an adapter that
breaks rule 4 is worse than no artefact at all: it asserts that an error path
executed when the code under test took a different branch entirely. That is not
a hypothetical. `doc.go:77` records the case — a write-ahead log classified
failures with `errors.As(err, &pathErr)`, its fault driver returned a bare
`fmt.Errorf`, an injected device error was classified as a torn tail, recovery
stopped early, and a record was silently dropped.

`doc.go:65` states the gap plainly: **rule 4 has no automatic check.** The
reason given is that no assertion can know what the real resource would have
returned. That is true of the error's *value* and false of its *predicates*, and
the sharper form of the rule at `doc.go:69` is exactly the testable one:

> An injected error must be indistinguishable from the real one to every
> predicate the code under test applies to it.

A predicate table is a check for that sentence.

### 3. The predicate table at the FS level — red first

`fs/os_test.go:102` is the existing check and it compares one field, `Op`, for
six `FS` methods, driving the real failure with a missing path. The predicates a
caller actually applies are wider:

| Predicate | Why a caller applies it |
|---|---|
| `errors.As(&*os.PathError)` | the graphdb case, verbatim |
| `errors.As(&*os.LinkError)` | `Rename` returns this and not `PathError` |
| `errors.Is(err, syscall.EIO)` | distinguishing a device error from a logical one |
| `errors.Is(err, syscall.ENOSPC)` | the retry-after-freeing-space branch |
| `os.IsNotExist` | first-run vs corrupted-store branch |
| `os.IsPermission` | a guard that must fail closed |
| `errors.Is(err, errors.ErrUnsupported)` | the optional-capability refusal |

Write the table before the repair and watch the rows nothing answers today.

**Acceptance**
- Every `fs.FS` method has a row for every predicate.
- For each row, the injected error and a real error of the same class agree —
  or the row records, in the table, why they cannot.
- The positive control holds: an error built with `fmt.Errorf` must make the
  table fail. That is the exact shape that cost graphdb a record, so a table
  that passes for it is measuring nothing.
- The `Op` comparison becomes one row of this table. Not a second table beside
  it — a second list is the drift `fs/contract_test.go` exists to remove.

### 4. The predicate table at the File level  ✔ done 2026-09-01

**It found a live defect on its first run.** `WriteAt` injected `Op: "writeat"`.
The os package reports `Op: "write"` for the same call, confirmed by three
independent routes — a closed handle, `/dev/full` and `/proc/self/mem`. Every
other method already agreed.

That is precisely the method-name form the FS-level test was written to prevent.
Its doc comment says a tidy-up making Op strings match method names "would pass
the entire suite while making every injected error a lie about what the
filesystem does". The FS level was guarded against it. The File level had no
rule 4 check of any kind, and that is where the form had already taken hold.

`fs/fault.go` now injects `"write"` from both WriteAt paths, and the doc comment
carries the measurement so nobody restores the method name from memory.

**A second Op string was left alone, deliberately.** The capability refusal
keeps `"writeat"`. It carries `errors.ErrUnsupported`, which no filesystem
produces for this call, so there is nothing for it to be indistinguishable
from — and its message names the capability the base lacks, which `"write"`
would misstate. `TestTheCapabilityRefusalNamesTheCapability` asserts that,
because a deliberate decision no test asserts is indistinguishable from the
accident it sits beside.

**The Op check and the predicate check are separate, and that split is the
design.** The Op string names the syscall and does not depend on the errno, so a
real error of any class is a correct source for it — a closed handle supplies
one for all seven methods. The predicate comparison needs a real error of the
*injected* class, and task 5's routes reach only `Read`, `Write` and `WriteAt`.
Without the split, `Sync`, `Truncate`, `Close` and `Seek` would have had no
check at all rather than a partial one, and the table would have been silent
about which.

Shipped in `fs/predicate_test.go`:
- `TestInjectedFileOpStringsMatchTheRealOnes` — all seven methods.
- `TestTheFileOpTableCoversTheFileInterface` — the reflective check, plus a
  hand-written line for `Seek` and `WriteAt`, which are members of no interface.
- `TestInjectedAndRealErrorsAnswerEveryPredicateTheSame` — eight predicates
  across five class-matched rows. All eight already agreed once the Op was
  fixed, which is the result worth recording: rule 4 held for the errno
  predicates and failed only on the string.
- A positive control proving at least one predicate separates a real filesystem
  error from a bare `errors.New` — the shape that cost the peer project a
  record.

**Still open at this level.** `Sync`, `Truncate`, `Close` and `Seek` are checked
for `Op` only. Closing that needs a route to a real `EIO` for them, and task 5
records that the obvious ones report `EINVAL` instead.

### 5. Real EIO and real ENOSPC controls  ✔ done 2026-09-01

**This task's premise was wrong, and measuring it is what corrected it.**

As written, this task claimed: "`EIO` and `ENOSPC` are not portably reachable,
and they are the two errors this package injects" — and it planned to measure
them once by hand and freeze the result as test data.

MEASURED 2026-09-01 on linux/amd64, kernel 7.1.10, go1.27.0. Both are reachable
from an ordinary unprivileged test, through two character devices:

	ENOSPC   write to /dev/full, which reports a full disk on every write
	EIO      read or write /proc/self/mem at offset 0, which is never mapped

	Write    /dev/full        *os.PathError{Op: "write", Err: ENOSPC(28)}
	WriteAt  /dev/full        *os.PathError{Op: "write", Err: ENOSPC(28)}
	Read     /proc/self/mem   *os.PathError{Op: "read",  Err: EIO(5)}
	Write    /proc/self/mem   *os.PathError{Op: "write", Err: EIO(5)}

Five consecutive runs, bit-identical. `yama`'s `ptrace_scope` restricts reading
*another* process's memory and does not reach `/proc/self/mem`; measured at
`ptrace_scope=1`.

So the deliverable changed from frozen data to a **live control**, which is
strictly better for the reason this repository already records about numbers: a
frozen figure cannot notice when the thing it describes changes.

What the routes do NOT reach, recorded rather than left to be discovered: `Sync`
and `Truncate` report `EINVAL` on both devices, `Close` reports nothing, and no
FS-level method reaches either errno (`Remove`/`Rename` give `EACCES`,
`MkdirAll`/`ReadDir` give `ENOTDIR`, `Stat` succeeds). Those rows of the
predicate table compare shape and `Op` only, and have to say so.

Shipped in `fs/realerr_test.go`:
- `onFullDevice` and `onUnmappedMemory`, the two routes.
- `TestTheRealErrorControlsProduceTheErrnoTheyClaim` — the control's own
  control, because a route that started returning `EINVAL` would hand every
  predicate row an error of the wrong class.
- `TestTheUnreachableRoutesAreStillUnreachable` — the "not reachable" list
  asserted rather than described, so a kernel that makes one of them work is
  reported as the good news it is.
- **On Linux a broken route fails; it does not skip.** A skip there would keep
  CI green over a gate that no longer gates. Elsewhere the devices do not exist,
  so the skip is honest and CI prints the per-leg count.

This task now runs BEFORE the predicate tables rather than after them, because
those tables can now compare injected-EIO against real-EIO instead of settling
for a shape comparison.

---

## Group C — the measure has to reach a caller's module

### 6. Tag the `tools` module

`tools/go.mod` declares `tool github.com/dd0wney/fault/tools/coupling`, and Go
resolves a module in a subdirectory through a tag prefixed with that
subdirectory. No `tools/v...` tag exists. So the tool directive is, today,
useful only inside this repository.

**Acceptance**
- A `tools/v0.1.0` tag on a commit CI has passed.
- A throwaway module with no `replace` acquires it by version, and the resolved
  version is the tag rather than a pseudo-version. This is the check that
  verified the root module's `v0.1.0`.
- `go tool coupling -h` runs in that module.

### 7. The caller's workflow, and a registry template

`docs/design/2026-08-29-coupling-coverage.md` specifies the tool for the person
building it. Nothing tells a stranger how to point it at their own code.

**Acceptance**
- The four commands, in order, with `-root`.
- A registry template carrying one `data` row, one `control` row, and one
  exemption row — with the note field filled, because §6 of the design says a
  row without a note is incomplete.
- The three exit codes, and the instruction to read the command's status rather
  than a pipeline's. `.handoff.md` records a merged PR that this mistake hid.
- The claim limit from §2.1, repeated rather than linked: no qualified Go
  compiler exists, so this produces no certification evidence.

---

## Group D — the new measure

### 8. A fixture that proves the hole — red first

Build a small module with two coupling sites: one reached only on the
terminating pass, one reached on a failing pass. Measure with `go tool coupling`
as it stands.

**Acceptance**
- The tool reports both as covered and exits 0.
- The fixture records that output verbatim, because that output *is* the defect.
- The two sites are indistinguishable in the report. If they can be told apart
  by any existing field, this plan's premise is wrong and the rest of the group
  should stop.

### 9. Per-pass capture, in a sibling package

The core has three exported names and they are meant to be permanent, so this
cannot go there. A sibling package wraps a sweep, clears the counters before
each pass, and writes a counter directory after it.

Two constraints, and each has to be a refusal:

- `ClearCounters` errors without `-cover` **and** without atomic counter mode.
  A package that swallowed either would emit a directory per pass whose contents
  are the accumulated total — which looks exactly like a correct split.
- A run that clears counters cannot also produce the ordinary `-coverprofile`,
  because the clear discards it. This is a separate CI step, not an extra flag
  on the existing one.

**Acceptance**
- Refuses without `-cover`, naming the flag.
- Refuses without atomic mode, naming `-covermode=atomic`.
- Writes exactly K+1 directories for a K-operation scenario, and identifies
  which one is the terminating pass.
- The dependency gate still reports standard library only.

### 10. The report that separates the two sets

	normal       the terminating pass alone
	robustness   passes 1..K, merged
	only-robust  covdata subtract: robustness minus normal

Then `covdata textfmt` on each, then `go tool coupling` on each.

**Acceptance**
- Every registry row gets one of three verdicts: exercised by a robustness pass;
  exercised **only** by the normal range pass; never exercised.
- The middle verdict is the finding this measure exists for, and the report says
  why in the row rather than in a footnote.
- No threshold. §3 of the design document is explicit: the tool reports and a
  human decides, because a floor set before the number is understood is how a
  coverage gate gets set wrong.
- The exit code distinguishes "no covered statement" from "no *robustness*
  statement". Collapsing them would make the new measure invisible to CI.

### 11. The selftest for the new gate

§7 of the design document states the rule and this repository states it in the
README: every gate ships with a selftest that runs first, because a gate that
cannot report a violation and a gate with nothing to report print the same
thing.

**Acceptance**
- One fixture per failure mode of the gate.
- A fixture where every coupling has a robustness statement, exiting 0.
- A fixture with empty counter directories, where the gate **refuses** rather
  than reporting 0/0. A zero-statement result is not a coverage result — §3 of
  the design document already makes this refusal for the original tool.
- The selftest runs before the gate in CI, as the other four do.

---

## Group E — the assertions a robustness pass has to make

### 12. `MaxOutstanding` on `alloc.Fault` and `sql.Fault`

`fs.Fault` answers three questions and each one arrived from a defect:

	Outstanding()     did anything leak
	OpenPaths()       which file
	MaxOutstanding()  was the question even live

The third exists because a final count of zero cannot separate "released
everything" from "never held anything", and within minutes of merging it found a
third reading in a caller — "held nothing at the moment I looked" — which is
weaker than both and looks identical.

`alloc.Fault` and `sql.Fault` each answer the first question only. The blind spot
is the same one, in two more packages, and both packages count something a
scenario can leak.

**Acceptance**
- Both methods exist, with the semantics `fs.Fault.MaxOutstanding` established.
- A test proves each reports 0 when the scenario acquired nothing — the reading
  the method exists to distinguish.
- A test proves each reports the high-water mark after acquire-and-release.
- `./alloc/` and `./sql/` re-measured, and the baseline rows updated with the
  date. Both currently sit at 1.00, so any survivor turns CI red.

### 13. The three questions, and which one a scenario never asked

`doc.go` names the assertion set a fault-injection loop needs — the operation
failed, nothing leaked, the state is still valid — and `alloc/alloc.go:55`
repeats it. A caller who writes only the first gets a green sweep over a
scenario that leaked on every pass.

The instruments are spread across three packages: `fs.Fault`, `alloc.Fault`,
`goroutine.Snapshot`. Nothing makes a caller consult all of them, and nothing
notices when one is missing.

**Recommended**: a checklist in the documentation *and* one helper, over
documentation alone. The measured history in this repository is that a rule
without a gate goes stale — `.handoff.md` records four instances in a single
day. `MaxOutstanding` is the proof that the "was the question live" reading is
real and mechanically checkable.

**Acceptance**
- The helper names a resource class that has a live count and no assertion.
- The helper names a scenario where no count ever rose above zero, which is the
  `MaxOutstanding` reading generalised across instruments.
- The helper makes **no** claim about the third question. Only the caller's own
  predicate can decide whether the state is valid, and a helper that pretended
  otherwise would manufacture exactly the false green this is written against.
- The documentation states which of the three the library cannot check, in the
  same voice §2.1 uses for the coupling tool.

---

## Group F — what the evidence does not establish

### 14. The claim limit, where a caller will read it

§2.1 of the coupling design states the limit for the tool. Nothing states it for
the sweep-as-robustness-evidence claim, and that claim is the larger one.

**Acceptance**
- A clean sweep proves the error paths **the scenario can reach**, and nothing
  about paths it cannot. The README already says this for crashes; it does not
  say it for coupling evidence.
- `fault/crash` rebuilds an approximation with a name, not the set of states a
  real power cut produces.
- No qualified Go compiler exists, so none of this is certification evidence.
- `README.md` links to it from the section that cites CERT, which is where a
  reader arrives with this question.

### 15. Record that MC/DC is out of scope, and why

DO-178C 6.4.4.c asks for modified condition/decision coverage at DAL A. Go has
no MC/DC tool, and `-covermode` counts statements rather than conditions. This
is worth stating rather than omitting: a reader who sees a DC/CC number and no
mention of MC/DC may reasonably infer the structural set is complete.

**Acceptance**
- The objective is named, and the fact that this project does not meet it.
- What `go tool coupling` measures, and what it does not — including the data
  coupling proxy that §5.4 of the design already admits.
- No document reads as though DC/CC coverage were the whole of structural
  coverage.

---

## Sequence

1 and 2 first. Then Group B, because the evidence has to be sound before anyone
produces it. Group C (6, 7) is independent of Group B.

**Group B is 3 → 5 → 4, and that is a correction.** As first written it was
3 → 4 → 5, on the assumption that a real EIO could not be reached from a test
and that tasks 3 and 4 would therefore compare shape only. Task 5 measured that
assumption and it was wrong on Linux, so the controls come before the tables
that consume them. The tables can now compare injected-EIO against real-EIO,
which is the full strength of rule 4 rather than a proxy for it.

Group D is strictly ordered: 8 → 9 → 10 → 11. Task 8 before 9 without exception
— a fixture written after the mechanism proves only that the mechanism is
present, and a fixture written before it proves the gap was real. This
repository has already recorded what a test written from inside the author's
model is worth.

Groups E and F are independent of everything else.

## Deliberately not in this plan

The earlier plan's two library-correctness items are held back rather than
dropped, and they belong in a Group G if this one lands:

- The rule 3 reflective check cannot see the optional method set. `Seek` and
  `WriteAt` are not members of `fs.File`, so `fs/positional_test.go:15` and
  `crash/positional_test.go:15` each carry a hand-written copy of the two
  interfaces.
- `fs.File` offers `WriteAt` and no `ReadAt`. A store reading at an absolute
  offset performs an operation no sweep can fail.

Neither blocks the evidence work, and both widen the surface that Group B has to
tabulate — which is the argument for doing them after task 4 rather than before.

## Open decisions

- **Task 9**: the sibling package's name. `fault/evidence` is the suggestion.
- **Task 13**: helper plus checklist, or documentation alone.
