// Package fault walks the point of failure through every operation a test
// scenario performs.
//
// SQLite states the loop plainly: "Rig the alternative interface to give an
// I/O error on the N-th system call, for N=1,2,3,.... Repeat until no I/O
// errors occur." The termination condition carries the meaning. When a run
// completes without the fault firing, N has passed the end of the sequence,
// and every error path the scenario can reach has been visited.
//
// A single fault test picks one point in that sequence, and picks it by luck.
// The interesting points sit inside a rename, between a write and its sync,
// and on the close after a failed sync. Nobody guesses those.
//
// # Using it
//
//	for n, p := range fault.Sweep(t) {
//		fs := faultfs.New(p, faultfs.OS())
//
//		err := store.OpenAndWrite(fs, dir)
//
//		// The loop body is the check. Assert the invariant here.
//		if !reopens(t, dir) {
//			t.Errorf("op %d: the store did not reopen: %v", n, err)
//		}
//	}
//
// The loop body decides what a broken invariant costs. Report it with
// [testing.T.Errorf] to walk the whole sequence and see every point that
// fails. Report it with [testing.T.Fatalf] to stop at the first one.
//
// # Why the filesystem is not here
//
// This package counts operations. It does not know what an operation is.
// A filesystem, an allocator, a network connection and a clock each supply
// their own adapter, and every adapter asks this package one question before
// each operation it performs: is this the one that must fail?
//
// That split is why the package is small. It also removes the one thing that
// would block a standard library proposal, because the standard library has
// no writable filesystem interface to name.
//
// # Writing an adapter
//
// An adapter wraps a real resource and calls [Points.Trip] before each
// operation it performs. Four rules make it work. Rule 3 is the one that
// breaks in practice, and it breaks silently.
//
//  1. Call Trip exactly once, immediately before each operation.
//
//  2. Skip the operation entirely when Trip returns true, and never retry it.
//     A retry inside the adapter hides the error path from the code under
//     test, which is the thing being tested.
//
//  3. Call Trip for EVERY operation, including ones the adapter performs
//     internally. An operation that skips the call is invisible to the sweep,
//     and the sweep then reports a clean walk over a sequence it never
//     visited. Nothing detects this except a test that counts operations
//     independently and compares.
//
//  4. Return an error the wrapped domain would really produce, never a
//     sentinel from this package. A filesystem returns [os.PathError] or
//     [os.LinkError] wrapping a syscall error. Only then can a store that
//     branches on the error be tested at all.
//
// Rule 4 has no automatic check, because no assertion can know what the real
// resource would have returned. The only way to get it right is to make the
// real thing fail and read what it reported.
//
// The full form of rule 4 is sharper than "return a realistic error", and it is
// worth stating on its own:
//
//	An injected error must be indistinguishable from the real one to every
//	predicate the code under test applies to it.
//
// Not merely "an error occurred". If the code asks errors.As(err, &pathErr),
// the adapter has to answer the way the kernel would. This rule arrived from
// the graphdb project, where breaking it cost real data: a write-ahead log
// classified failures with
//
//	var pathErr *os.PathError
//	return errors.As(err, &pathErr)   // true means "a resource failed"
//
// and its fault driver returned a bare fmt.Errorf. So an injected device error
// during recovery was classified as a torn tail instead of a resource failure.
// Recovery stopped early, the next append reused a log sequence number that
// already existed on disk, and the record was silently dropped by the next
// recovery. The tests that injected those faults passed while exercising the
// wrong branch entirely.
//
// The defect is invisible from the operation's side: the call returned an
// error, as intended. It appears only when you look at what the caller asks of
// the error. So measure both -- what the real operation returns, and what the
// code under test interrogates it for.
//
// Rule 2 has one principled exception, and an adapter that takes it must say
// so at the method. A failed close(2) still releases its descriptor, because
// POSIX guarantees it, so a Close that skips the release leaks one handle per
// pass over a walk that may run hundreds of passes. The rule protects the
// caller from observable effects of a failed operation; it does not stop the
// operating system reclaiming its own.
//
// An adapter returning another interface -- a filesystem returning an open
// file -- must wrap what it returns and pass on the same [Points]. Both share
// one sequence, so an open, a write, a sync and a close are operations 1 to 4
// of one scenario rather than two separate counts. A method that returns an
// unwrapped value is a silent hole in the injection.
//
// # Limitations
//
// A sweep is meaningful only when one goroutine performs the operations.
//
// [Points] is safe for concurrent use, which prevents a data race. It does not
// make the walk meaningful when several goroutines share one: with concurrency
// the N-th operation overall is a different operation on every run, because
// the scheduler picks the interleaving. A sweep over that visits an arbitrary
// subset of the error paths while appearing to visit all of them -- and it
// still terminates, and it still passes.
//
// A concurrent scenario needs a per-actor counter and a key naming the actor,
// the operation type and the occurrence within that actor. That is not in this
// package.
//
// A sweep injects returned errors, not crashes.
//
// An error return is a cooperative failure: the caller is told, and it gets to
// unwind, clean up and report. A crash is not cooperative, and that difference
// is what makes the two find different defects. Every defence built on defer,
// on a cleanup path, or on error handling works against the first and is
// simply absent for the second.
//
// So a clean sweep does not verify a store's durability. Consider a store that
// writes straight to its destination file instead of writing to a temporary
// one and renaming it into place. That store is unsafe, and a sweep will not
// say so: after a failed write its cleanup deletes the partial file, so no bad
// state ever survives to be observed. The same store loses data on a power cut
// between the write and the sync.
//
// Reaching that class of defect needs the state captured at each operation and
// then reopened as though the process had died -- SQLite runs it as a third
// loop, beside its out-of-memory and I/O-error loops. This package does not.
//
// A sweep also injects whole-operation failures only. A real write may write
// fewer bytes than asked and report no error at all, and a store that ignores
// the returned count is invisible here.
package fault
