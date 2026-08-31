// Package sql injects faults into a [database/sql] driver.
//
// It is the third adapter in this module, after
// [github.com/dd0wney/fault/fs] and [github.com/dd0wney/fault/alloc], and the
// first written against an interface this module did not design. The design
// document is docs/design/2026-08-31-sql-adapter.md, which records six
// decisions and the measurement behind each.
//
// # Using it
//
//	db, f := faultsql.OpenDB(p, connector)
//	defer db.Close()
//
//	for _, p := range fault.Sweep(t) {
//		db, f := faultsql.OpenDB(p, connector)
//		err := store.Save(db, "v2")
//		if f.Err() != nil {
//			t.Fatal(f.Err()) // the sweep could not describe the run
//		}
//		if f.Outstanding() != 0 {
//			t.Errorf("%d connections leaked after %v", f.Outstanding(), err)
//		}
//		db.Close()
//	}
//
// [OpenDB] uses sql.OpenDB rather than sql.Register and sql.Open. Register
// installs a driver under a global name and panics on a duplicate, so a test
// using it would have to invent a unique name per run and could never remove
// it.
//
// # What counts as one operation
//
// Every call the caller makes counts: Connect, Prepare, PrepareContext, Begin,
// BeginTx, Ping, Exec, Query, ExecContext, QueryContext, Commit, Rollback, and
// every Close.
//
// Three things do not, and each is a deliberate exception with a test that
// fails if it stops being one.
//
// NumInput and [driver.Rows].Columns are properties rather than operations.
// database/sql asks for both before performing anything, and a driver answers
// from memory.
//
// ResetSession is the sharp one, and it is the opposite of the rule Begin
// follows. The POOL calls it, not the caller, and it decides when from its own
// state: whether a connection was reused, how long it sat, what else the
// program did. Counting it would make the N-th operation a different operation
// between two runs of one scenario. It is still forwarded, because omitting
// the method would stop database/sql resetting the base driver at all.
//
// [driver.Rows].Next consumes exactly ONE index per result set, on its first
// call, and not one per row. Per row was the obvious reading and it is wrong
// twice: a 600-row query would pass the core's maxOps of 512 and the sweep
// would report non-termination, which reads exactly like a defect in the code
// under test; and the count would then follow the data, so adding a row to a
// fixture would move every later armed point.
//
// # Limitations
//
// Stated plainly, because the failure mode of a testing library is a false
// green.
//
// Nothing about [driver.ErrBadConn] retries. database/sql retries a bad
// connection — sql.go:1579 runs its body twice and then calls
// fn(alwaysNewConn) once more, so one armed operation would become up to three
// driver calls and the count would stop meaning anything. [ErrInjected] is
// deliberately not one, and a test asserts that rather than trusting it.
//
// Nothing under a pool wider than one connection. [OpenDB] sets
// SetMaxOpenConns(1), and a caller may widen it on the returned handle with
// nothing in database/sql reporting that back here. [Fault.Err] therefore
// refuses when two connections are live at once: it measures the property
// rather than the setting. A caller who wants concurrency wants
// [github.com/dd0wney/fault/role], which exists for exactly this.
//
// Nothing about a failure at an arbitrary row of a large result set, unless
// the caller names it with [NewAtRow]. A named row past the end of the result
// set is REFUSED rather than skipped in silence: the pass consumed an
// operation index and injected nothing, so it asserted nothing while reporting
// a pass.
//
// Nothing about a result set the caller never iterates. Next consumes the
// index, so a Rows opened and closed without a Next takes none. That follows
// the program's structure rather than its data, which is why it is a limit
// and not a defect.
//
// Nothing about an interface the wrapper does not implement. database/sql
// type-asserts for optional interfaces at run time and changes its own path on
// the answer: sql.go:1777 falls back to Prepare, then Query, then Close when a
// Conn implements neither QueryerContext nor Queryer, turning one driver call
// into three. The wrapper implements the set that changes the call count, and
// a test reflects over the wrapped driver and this one and fails on any
// difference. That test is the guarantee, not this sentence.
//
// Nothing the check does not assert. This is the same trap the core
// documentation names for every adapter: a method that calls Trip and ignores
// the answer keeps the pass count exactly right, so counting passes never
// proves anything.
package sql
