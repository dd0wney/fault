// Package role walks the point of failure through one actor's operations while
// the others run untouched.
//
// [github.com/dd0wney/fault.Sweep] counts every operation the process performs,
// which is exactly right for one goroutine and meaningless for several: with
// concurrency the N-th operation overall is a different operation on every run,
// because the scheduler picks the interleaving. A sweep over that visits an
// arbitrary subset of the error paths while appearing to visit all of them --
// and it still terminates, and it still passes.
//
// A per-actor counter is stable as long as each actor's own sequence is stable.
// [Sweep] verifies that rather than assuming it. See "Stability" below, which
// is the part of this package that earns its existence.
//
// # Using it
//
//	for n, p := range role.Sweep(t, "flush") {
//		store := open(t, adapterFor(p))
//
//		go compact(store)   // runs untouched
//		flush(store)        // operation n of this role fails
//
//		if !reopens(t, dir) {
//			t.Errorf("flush op %d: the store did not reopen", n)
//		}
//	}
//
// # Not frozen
//
// The core fault package is a candidate for the Go standard library and its
// three exported names are permanent. This package is not that. Its API will
// change, and it says so here so that nobody mistakes the two.
//
// # Stability
//
// This package is only sound when the target role's own operation sequence is
// the same on every run. That is a property of the caller's scenario, not of
// this package, so it cannot be assumed -- and a sweep whose actor is not
// deterministic terminates, passes, and proves nothing, which looks identical
// to one that works.
//
// [Sweep] therefore re-checks it on every pass, comparing the role's trace with
// the previous pass up to the injection point. Beyond that point the sequences
// legitimately diverge, because the fault changed what happened next.
//
// # The trace element
//
// An adapter names each operation with a short string, and that string must be
// a function of what the actor ASKED FOR, and of nothing the environment
// supplied.
//
//	adapter      correct                   wrong, and why
//	filesystem   "open wal.log"            "read 4096 bytes" -- a short read is
//	                                       the environment answering
//	allocator    "Bytes(4096)"             the returned slice or pointer
//	clock        the requested duration    the observed elapsed time
//
// Getting this wrong makes every sweep unsound in a way that presents as a
// flaky test.
//
// An adapter that cannot produce a request-only name must not offer a role
// sweep at all. A network connection is the example: "the third read from this
// connection" is a request, but its size is the peer's choice. A stability
// check that merely fires often is worse than one that refuses at construction,
// because the first teaches people to ignore it.
//
// This design came from the graphdb project, which built it first and supplied
// the stability check, the invariant above, and the reason an unrecognised
// operation needs its own counter.
package role
