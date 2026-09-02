// Package leak names a resource class that has a live count and no
// assertion, and a scenario where no count ever rose above zero.
//
// The core package's own documentation, under "Counting what the adapter
// hands out", and [github.com/dd0wney/fault/alloc]'s own doc both name the three
// assertions a fault-injection loop needs: the operation failed, nothing
// leaked, the state is still valid. The instruments for the second are
// spread across three packages -- [github.com/dd0wney/fault/fs.Fault],
// [github.com/dd0wney/fault/alloc.Fault] and
// [github.com/dd0wney/fault/sql.Fault], each with Outstanding and
// MaxOutstanding, and [github.com/dd0wney/fault/goroutine.Snapshot] with
// Since -- and nothing made a caller consult all of them, or noticed when
// one was missing. A caller who wrote only the first assertion got a
// green sweep over a scenario that leaked on every pass.
//
// # The checklist
//
// A fault-injection loop owes three assertions, in this order:
//
//  1. The operation failed. Assert the injected error by identity --
//     errors.Is against the sentinel the adapter documents, never a bare
//     "err != nil" -- which [github.com/dd0wney/fault.Points] makes
//     possible: it fires deterministically, so the caller knows in advance
//     which operation the sweep is failing and what that failure is
//     supposed to look like.
//  2. Nothing leaked. [Check] over every counter the scenario holds, and
//     [Goroutines] over a snapshot taken before the scenario ran.
//  3. The state is still valid. The caller's own predicate. This library
//     cannot write it, and does not try to.
//
// # Using it
//
//	for n, p := range fault.Sweep(t) {
//		before := goroutine.Take()
//		fsys := faultfs.New(p, faultfs.OS())
//
//		store, err := Open(fsys, dir)
//		if !errors.Is(err, syscall.EIO) {
//			t.Fatalf("op %d: got %v, want the injected EIO", n, err)
//		}
//
//		leak.Check(t, fsys)
//		leak.Goroutines(t, before, 2*time.Second)
//	}
//
// # The limit
//
// [Report] and [Check] make NO claim about the third question. Only the
// caller's own predicate can decide whether the state is still valid, and
// a helper that pretended otherwise would manufacture exactly the false
// green this package is written against -- the same limit the coupling
// tool states about itself, in §2.1 of docs/design/2026-08-29-coupling-coverage.md:
// a clean run proves what the check looked at, and nothing about what it
// did not.
//
// [Goroutines] carries a narrower limit of its own: there is no "was the
// question live" reading for a goroutine count, because
// [github.com/dd0wney/fault/goroutine.Snapshot] does not count how many
// goroutines started, only which ones are still running.
// [Counter.MaxOutstanding] answers that question for a resource counter;
// nothing here can answer it for a goroutine, so this package does not
// claim to.
package leak
