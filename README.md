# fault

Walk the point of failure through every operation your code performs.

```go
for n, p := range fault.Sweep(t) {
	fs := faultfs.New(p, faultfs.OS())

	err := store.OpenAndWrite(fs, dir)

	// The loop body is the check. Assert the invariant here.
	if !reopens(t, dir) {
		t.Errorf("op %d: the store did not reopen: %v", n, err)
	}
}
```

The loop fails the first operation, then the second, then the third, and it
stops when the fault runs off the end of the sequence. Every error path the
scenario can reach has then been executed — including the ones inside a rename,
between a write and its sync, and on the close after a failed sync.

Three exported names. No dependencies outside the standard library.

## Why

A single fault test picks one point in the sequence, and picks it by luck.
Nobody guesses the interesting ones.

SQLite documents the loop it runs:

> Rig the alternative interface to give an I/O error on the N-th system call,
> for N=1,2,3,.... Repeat until no I/O errors occur.

The termination condition carries the meaning. A run that completes without the
fault firing proves N has passed the end of the sequence.

## What it finds that a checker cannot

Static analysis reads your code. It does not know which paths execute.

CERT states three of this library's properties as rules — `ERR33-C` (detect and
handle standard library errors), `MEM31-C` (free memory when no longer needed),
`FIO42-C` (close files when no longer needed). There is no CERT standard for Go;
the coverage is C, C++, Java, Android, Perl and Fortran.

`ERR33-C` a checker can largely find by reading. The other two, **on the error
path**, it generally cannot: whether the buffer is freed or the file is closed
depends on control flow through a failure that never happens in a normal run.
A checker sees the code. A sweep makes the path execute.

That is the claim, and it is narrow on purpose. This is not a Go CERT — it holds
no rules and covers three of several hundred. It is the apparatus that tests a
few of them.

## Compared with

| | How it works |
|---|---|
| `pingcap/failpoint`, `etcd-io/gofail` | A developer hand-places a named failpoint at each call site, and a test arms it by name. |
| `cockroachdb/pebble` `errorfs` | The same idea, internal to Pebble, not exported. |
| `spf13/afero` | A filesystem abstraction with an in-memory backend. No fault injection. |
| **this** | Automatic — no hand-placed sites. Exhaustive — every operation, in turn. And it terminates on its own and says so. |

> A failpoint fails where you thought to put one. A sweep fails everywhere,
> including the places you did not think of, and it tells you when it has run
> out of places.

## Layout

| Package | |
|---|---|
| `fault` | Counts operations and decides which one must fail. Zero dependencies. Three exported names, and they are meant to stay that way. |
| `fault/fs` | Filesystem adapter. Wraps a real filesystem rather than replacing it, so everything not failed is served for real. |
| `fault/alloc` | Allocator adapter, with both of SQLite's out-of-memory loops and an outstanding-allocation count. |
| `fault/role` | Per-actor sweeps for concurrent scenarios, with the stability check that makes them sound. |
| `fault/tools` | Build-time gates. A separate module, and not part of the library. |

Adapters need `Points` and `Trip` and nothing else, so anyone can write one in
their own module. Run `go doc github.com/dd0wney/fault` for the contract.

## What it does not do

Stated plainly, because the failure mode of a testing library is a false green.

**Errors, not crashes.** An error return is a cooperative failure: the caller is
told and gets to unwind. A crash is not. So a clean sweep does **not** verify
durability — a store that writes straight to its destination file is unsafe, and
a sweep will not say so, because its cleanup deletes the partial file. SQLite
runs a third loop for this. This library does not.

**Short writes, but only the shape a real file produces.** `fs.NewShortWrite`
moves part of a buffer and reports ENOSPC, the way a full disk does, so a torn
record on disk is reachable. A short write carrying **no** error is not:
`os.File` never returns that pair, because it synthesises `io.ErrShortWrite`
whenever the count falls short and the syscall reported nothing. Another
`fs.File` implementation can, and a caller that checks the error and ignores the
count is invisible here.

**One goroutine, in the core.** Under concurrency the N-th operation overall is
a different operation on every run. A sweep over that visits an arbitrary subset
of the error paths while appearing to visit all of them — and it still
terminates, and it still passes. `fault/role` sweeps one actor at a time and
verifies on every pass that the actor's own sequence is stable, which is the
property that makes a per-actor count mean anything.

## Status

Early. `v0`, and the API of everything outside the core will change.

The core's three names are intended to be permanent, and the long-term aim is a
Go standard library proposal. That is a low-probability goal — every `testing/*`
package was written by the Go team — and it is worth stating because it explains
the design: three exported names, zero dependencies, and adapters kept one
package away.

## Development

```
go test ./...                          # 78 tests
./scripts/mutation-selftest.sh         # prove the mutation gate can fail
./scripts/mutation.sh                  # then run it
./scripts/no-external-deps.sh          # standard library only
```

Every gate ships with a selftest that runs **first**, because a gate that cannot
report a violation and a gate with nothing to report print the same thing.

## Credits

The design of `fault/role` — the per-actor sweep, its stability check, and the
rule that a trace element must describe what an actor asked for and nothing the
environment supplied — came from the [graphdb](https://github.com/dd0wney/graphdb)
project, which built it first.

## Licence

BSD-3-Clause.
