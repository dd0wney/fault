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
// Rule 2 has two principled exceptions, and an adapter that takes one must say
// so at the method.
//
// The first: a failed close(2) still releases its descriptor, because
// POSIX guarantees it, so a Close that skips the release leaks one handle per
// pass over a walk that may run hundreds of passes. The rule protects the
// caller from observable effects of a failed operation; it does not stop the
// operating system reclaiming its own.
//
// The second: an operation that fails partway leaves what it already did. A
// real write can move some of its buffer and then fail, and an adapter that
// models one must leave those bytes where a real write would. Here the effect
// is not incidental to the fault -- it is the fault. A torn record on disk is
// the state the caller has to survive, and an injection that tidied it away
// would test nothing the whole failure does not already test. See
// fs.NewShortWrite.
//
// Both exceptions meet the same test: the effect is one that a real failure of
// that operation also leaves behind. An adapter may not invent an effect of its
// own.
//
// An adapter returning another interface -- a filesystem returning an open
// file -- must wrap what it returns and pass on the same [Points]. Both share
// one sequence, so an open, a write, a sync and a close are operations 1 to 4
// of one scenario rather than two separate counts. A method that returns an
// unwrapped value is a silent hole in the injection.
//
// An adapter needs nothing from this package beyond [Points] and [Points.Trip],
// so it can live in any module. The adapters here are ordinary consumers of the
// published API and hold no privileged access.
//
// # Counting what the adapter hands out
//
// A fault-injection loop needs three assertions, not one: the operation failed,
// nothing leaked, and the state is still valid. This package can help with none
// of them -- it does not know what a resource is -- but an adapter that hands
// something out can answer the second, and every adapter here does:
//
//	Outstanding()     how many are held now
//	MaxOutstanding()  the most held at once, which never falls
//
// The second method is not a convenience. Outstanding() == 0 at the end of a
// scenario is the PASS condition for "nothing leaked", and it is also exactly
// what a scenario that acquired nothing returns. The two are indistinguishable,
// so a sweep over a component that took no resource reports a clean leak check
// having compared 0 against 0:
//
//	if adapter.MaxOutstanding() == 0 {
//		t.Errorf("nothing was ever acquired, so the leak check proved nothing")
//	}
//
// An adapter offering both lets a caller ask that question the same way of every
// resource class it uses. The third assertion has no such shape: only the
// caller's own predicate can decide whether the state is valid, and an adapter
// that claimed otherwise would manufacture the false green all of this exists to
// prevent.
//
// # Testing an adapter
//
// Drive the adapter's own tests through [Sweep]. There is no exported way to
// arm a [Points] directly, and that is deliberate: an adapter tested against a
// hand-armed point is tested against a situation the sweep may never produce.
//
// One trap, which the author of this package fell into twice. Perform the
// operations on EVERY pass, and make only the assertions conditional:
//
//	for n, p := range fault.Sweep(t) {
//		err := doTheThing(adapter.New(p, base))  // every pass
//		if n != 1 {
//			continue                             // assert on one
//		}
//		...
//	}
//
// Putting the `continue` first looks equivalent and is not. The skipped passes
// then perform no operations at all, so the sweep terminates on the first one
// and reports that the scenario proved nothing -- which is correct, and reads
// like a bug in the sweep rather than in the test.
//
// Two useful shapes fall out. A scenario performing exactly one operation runs
// exactly two passes, so a pass count of two proves the adapter calls Trip at
// all. And a method that calls Trip but ignores the answer keeps the pass count
// exactly right, so counting passes never proves the injected error is
// returned: assert on the error as well.
//
// # Security properties
//
// Three defect classes are reachable by a sweep and by almost nothing else.
// None needs anything beyond [Sweep] and [Points.Trip] -- the predicate in the
// loop body is what changes.
//
// # Fail closed
//
// A guard that reads its policy from somewhere can fail to read it. Whether it
// then denies or allows is a decision, and it is usually made by whichever
// branch the author wrote first:
//
//	for n, p := range fault.Sweep(t) {
//		allowed, err := guard.Check(faultfs.New(p, faultfs.OS()), bob, secret)
//		if err != nil && allowed {
//			t.Errorf("op %d failed and the guard still allowed access: %v", n, err)
//		}
//	}
//
// Every operation in the authorisation path -- opening the policy, reading it,
// closing it -- becomes a separate test, in order.
//
// # Error indistinguishability
//
// A guard may leak the existence of a resource by answering differently for one
// that exists and one that does not. Ordinary tests miss it because both
// answers are errors; the leak is in which error.
//
// Compare what two principals see, rather than asserting what either should
// see. This needs TWO sweeps, compared position by position:
//
//	answers := func(target Resource) []string {
//		var out []string
//		for _, p := range fault.Sweep(t) {
//			fsys := faultfs.New(p, faultfs.OS())
//			out = append(out, describe(guard.Check(fsys, bob, target)))
//		}
//		return out
//	}
//
//	existing, absent := answers(exists), answers(doesNotExist)
//	for n := range existing {
//		if existing[n] != absent[n] {
//			t.Errorf("op %d: an unauthorised caller can tell the two apart: %q vs %q",
//				n+1, existing[n], absent[n])
//		}
//	}
//
// Two sweeps, and not two calls inside one. A single Points counts across
// BOTH calls, so arming operation N fails exactly one of them and never the
// other. The two answers then differ on every pass by construction, and the
// predicate reports a leak that is an artefact of where the ordinal landed.
// An earlier version of this documentation showed the one-sweep form and was
// wrong; it was caught by running it against a third-party application.
//
// Each sweep arms operation N of its OWN run, so comparing position n with
// position n is meaningful. If the two principals perform different numbers of
// operations, the lengths differ, and that is itself the leak.
//
// Give the comparison a control. Run it once with two inputs that SHOULD
// differ -- a right password and a wrong one -- and confirm it says so. A
// comparison that cannot report a difference cannot report a leak.
//
// Like the adapter predicate table, this asserts agreement rather than
// correctness, so it cannot encode the same misunderstanding twice. It is worth
// the most on the passes where the filesystem misbehaves: a guard that answers
// uniformly on the happy path often stops doing so once a read fails.
//
// # Cleanup on the unwind path
//
// A descriptor, a lock, or a temporary file holding plaintext leaks on the path
// nobody tests, which is the error path. An adapter that counts what it handed
// out reports it for free -- see [github.com/dd0wney/fault/alloc.Fault.Outstanding].
//
// # Prior art for these properties
//
// CERT's secure coding standards state three of them as rules. There is no Go
// standard -- the coverage is C, C++, Java, Android, Perl and Fortran -- but
// the rules describe properties rather than syntax, and a sweep is a way to
// TEST what they state:
//
//	ERR33-C  Detect and handle standard library errors
//	MEM31-C  Free dynamically allocated memory when no longer needed
//	FIO42-C  Close files when they are no longer needed
//	FIO46-C  Do not access a closed file
//
// ERR33-C is the one this package exists for. A sweep makes every library call
// fail in turn, so a caller that ignores a return value is caught on the pass
// that fails it, and on no other. MEM31-C and FIO42-C are violated almost
// exclusively on error paths -- the happy path frees and closes -- which is why
// an adapter that counts what it handed out finds them and ordinary tests do
// not. See [github.com/dd0wney/fault/alloc.Fault.Outstanding].
//
// A static checker can find some violations of all four by reading the code. A
// sweep executes the path instead, which finds the ones no checker can see and
// misses the ones no test reaches.
//
// // # What this does not cover
//
// Untrusted input is a different axis, and go test -fuzz is the tool for it.
// The two are complementary: a fuzzer varies what the code is given, and a
// sweep varies what the environment does to it while it works.
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
// [github.com/dd0wney/fault/crash] is that third loop, and it carries its own
// limits, which are worth reading before a clean run there is trusted.
//
// A sweep injects one shape of short write and not the other.
//
// fs.NewShortWrite moves part of a buffer and reports ENOSPC, which is what a
// full disk does to a real write. What stays out of reach is a short write
// carrying no error. os.File never produces that pair: it synthesises
// io.ErrShortWrite whenever the count falls short and the syscall itself
// reported nothing. Another fs.File implementation can produce it, and a caller
// that checks the error and ignores the count is invisible here.
package fault
