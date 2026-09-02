package sql_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/dd0wney/fault"
	faultsql "github.com/dd0wney/fault/sql"
)

// THE DECISION OF FORK 1, asserted rather than trusted.
//
// database/sql retries a bad connection. sql.go:1579 runs its loop body twice
// and then calls fn(alwaysNewConn) once more, so an injected error satisfying
// errors.Is(err, driver.ErrBadConn) becomes up to three driver calls, and the
// operation the sweep armed is not the operation the caller sees fail.
//
// The identity check is cheap and weak. The behavioural half is the one that
// matters, and it works because Trip returns true AT MOST ONCE: if
// database/sql retried, the second Trip would return false, the retry would
// reach the base, and the ping would SUCCEED. So a nil error here IS the
// retry, observed rather than inferred.
func TestTheInjectedErrorIsNotABadConnection(t *testing.T) {
	if errors.Is(faultsql.ErrInjected, driver.ErrBadConn) {
		t.Fatal("ErrInjected is a driver.ErrBadConn, so database/sql will swallow it and retry")
	}

	for n, p := range fault.Sweep(t) {
		base := &testDriver{}
		db, _ := faultsql.OpenDB(p, base)

		err := db.PingContext(context.Background())
		_ = db.Close()

		if n != 1 {
			continue // only the first pass fails the first operation
		}
		if err == nil {
			t.Error("the ping succeeded with its connect armed, which means database/sql swallowed the injected error and retried on a fresh connection")
		}
		if connects, _ := base.counts(); connects != 0 {
			t.Errorf("the base driver saw %d connect(s), want 0 — a tripped connect must not reach the base, and a retry is the only way it can", connects)
		}
	}
}

// A FAILED connect handed nothing out, so it counts nothing. Counting it would
// report a leak on every pass that fails a connect, which is most of them.
func TestAFailedConnectCountsNoOutstandingConnection(t *testing.T) {
	for n, p := range fault.Sweep(t) {
		base := &testDriver{}
		db, f := faultsql.OpenDB(p, base)

		err := db.PingContext(context.Background())

		if n == 1 {
			if !errors.Is(err, faultsql.ErrInjected) {
				t.Errorf("the armed connect: err = %v, want ErrInjected", err)
			}
			if got := f.Outstanding(); got != 0 {
				t.Errorf("Outstanding() = %d after a failed connect, want 0", got)
			}
		}
		_ = db.Close()
	}
}

// A FAILED close still decrements, because the connection is gone either way,
// and the real Close runs first so the base sees it too.
//
// This drives the connector directly rather than through *sql.DB. The pool
// decides when to close, so a test that went through it would be asserting the
// pool's policy and not this package's rule.
func TestAFailedCloseStillDecrements(t *testing.T) {
	reached := false

	for n, p := range fault.Sweep(t) {
		base := &testDriver{}
		f := faultsql.New(p, base)

		c, err := f.Connect(context.Background()) // operation 1
		if err != nil {
			continue // this pass failed the connect; there is nothing to close
		}
		closeErr := c.Close() // operation 2

		if n != 2 {
			continue
		}
		reached = true
		if !errors.Is(closeErr, faultsql.ErrInjected) {
			t.Errorf("Close() = %v, want the injected error", closeErr)
		}
		if got := f.Outstanding(); got != 0 {
			t.Errorf("Outstanding() = %d after a failed close, want 0 — every driver releases the connection either way", got)
		}
		if _, closes := base.counts(); closes != 1 {
			t.Errorf("the base saw %d close(s), want 1 — a tripped close must still close the real connection", closes)
		}
	}

	// Without this the test passes when the sweep never reaches operation 2,
	// which is the shape of a check that asserts nothing and reports a pass.
	if !reached {
		t.Fatal("the sweep never armed operation 2, so nothing above was asserted")
	}
}

// THE DECISION OF FORK 2, measured rather than configured.
//
// OpenDB sets SetMaxOpenConns(1), and a caller can widen it afterwards. The
// refusal therefore counts live connections instead of reading the setting: a
// gate that read the setting would be correct about the pool it created and
// silent about the pool in use.
func TestTwoLiveConnectionsAreRefused(t *testing.T) {
	base := &testDriver{}
	f := faultsql.New(&fault.Points{}, base)

	if err := f.Err(); err != nil {
		t.Fatalf("a fresh Fault already holds a refusal: %v", err)
	}

	a, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if err := f.Err(); err != nil {
		t.Fatalf("one live connection was refused, so the guard fires too early: %v", err)
	}

	b, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("second connect: %v", err)
	}
	if f.Err() == nil {
		t.Error("two live connections were not refused, so an unstable operation index would go unreported")
	}

	_ = a.Close()
	_ = b.Close()

	// The refusal is held, not cleared. The run it describes already happened,
	// and a guard that forgot it would report a pass for that run.
	if f.Err() == nil {
		t.Error("closing the connections cleared the refusal, but the run it describes still happened")
	}
}

// THE DECISION OF FORK 3, and the part of it that must not be skipped.
//
// database/sql type-asserts for optional interfaces at run time and changes its
// own path on the answer. A wrapper implementing fewer than the driver beneath
// it changes the program under test, and everything still compiles.
//
// THIS TEST COVERED ONE OF THREE LEVELS UNTIL 2026-08-31. Every interface it
// named belonged to driver.Conn. The wrapper's stmt and rows types implemented
// only their required methods, and nothing looked. The commit that closed the
// Conn level said "fork 3 is closed", which was true of what this test read and
// not of the package.
//
// The three levels and what each costs:
//
//	Conn   sql.go:1777 falls back to Prepare, then Query, then Close when a
//	       Conn implements neither QueryerContext nor Queryer. ONE driver call
//	       becomes THREE, and every armed point after it moves.
//	Stmt   ctxutil.go:81 calls si.Query(dargs) when a Stmt has no
//	       StmtQueryContext. The call count does not change and THE CONTEXT IS
//	       DROPPED, so a caller's cancellation stops working once wrapped.
//	Rows   sql.go:3307 and :3312 type-assert for RowsColumnTypeScanType and
//	       RowsColumnTypeDatabaseTypeName. A wrapper without them makes
//	       Rows.ColumnTypes() return less than the unwrapped driver does.
//
// The Conn level is the only one that moves the operation count. The other two
// degrade the driver silently, which is worse to find and no less real.
func TestTheWrapperForwardsWhatTheBaseImplements(t *testing.T) {
	base := &testDriver{}
	f := faultsql.New(&fault.Points{}, base)

	wrappedConn, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = wrappedConn.Close() }()

	bareConn, err := base.Connect(context.Background())
	if err != nil {
		t.Fatalf("base connect: %v", err)
	}
	defer func() { _ = bareConn.Close() }()

	t.Run("Conn", func(t *testing.T) {
		compareInterfaces(t, wrappedConn, bareConn, connInterfaces(), map[string]bool{})
	})

	t.Run("Stmt", func(t *testing.T) {
		wrapped, err := wrappedConn.Prepare("select n")
		if err != nil {
			t.Fatalf("wrapped prepare: %v", err)
		}
		defer func() { _ = wrapped.Close() }()
		bare, err := bareConn.Prepare("select n")
		if err != nil {
			t.Fatalf("bare prepare: %v", err)
		}
		defer func() { _ = bare.Close() }()

		compareInterfaces(t, wrapped, bare, stmtInterfaces(), map[string]bool{})
	})

	t.Run("Rows", func(t *testing.T) {
		wrapped := queryRows(t, wrappedConn)
		defer func() { _ = wrapped.Close() }()
		bare := queryRows(t, bareConn)
		defer func() { _ = bare.Close() }()

		compareInterfaces(t, wrapped, bare, rowsInterfaces(), map[string]bool{})
	})
}

func queryRows(t *testing.T, c driver.Conn) driver.Rows {
	t.Helper()
	q, ok := c.(driver.QueryerContext)
	if !ok {
		t.Fatal("no driver.QueryerContext, so the rows level cannot be reached")
	}
	rs, err := q.QueryContext(context.Background(), "select n", nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return rs
}

// compareInterfaces fails on any difference between what the base implements
// and what the wrapper does.
//
// known lists the differences that are scheduled rather than accidental. An
// entry that the wrapper has since implemented is itself an error, so a closed
// gap cannot sit in the list pretending to be open.
func compareInterfaces(t *testing.T, wrapped, bare any, optional map[string]reflect.Type, known map[string]bool) {
	t.Helper()
	wt, bt := reflect.TypeOf(wrapped), reflect.TypeOf(bare)

	// The positive control. If the base implemented none of these, every
	// assertion below would pass while proving nothing at all.
	implementedByBase := 0
	for _, iface := range optional {
		if bt.Implements(iface) {
			implementedByBase++
		}
	}
	if implementedByBase == 0 {
		t.Fatalf("%T implements none of the %d optional interfaces, so this check cannot detect a wrapper that forwards none", bare, len(optional))
	}

	for name, iface := range optional {
		baseHas, wrapperHas := bt.Implements(iface), wt.Implements(iface)
		switch {
		case baseHas && !wrapperHas && !known[name]:
			t.Errorf("the base implements driver.%s and %T does not, so database/sql takes a different path through the wrapper", name, wrapped)
		case !baseHas && wrapperHas:
			t.Errorf("%T implements driver.%s and the base does not, so it answers for a capability that is not there", wrapped, name)
		case baseHas && wrapperHas && known[name]:
			t.Errorf("driver.%s is listed as a known gap and %T now implements it — remove it from `known`", name, wrapped)
		}
	}
}

func connInterfaces() map[string]reflect.Type {
	return map[string]reflect.Type{
		"ConnPrepareContext": reflect.TypeOf((*driver.ConnPrepareContext)(nil)).Elem(),
		"ConnBeginTx":        reflect.TypeOf((*driver.ConnBeginTx)(nil)).Elem(),
		"QueryerContext":     reflect.TypeOf((*driver.QueryerContext)(nil)).Elem(),
		"Queryer":            reflect.TypeOf((*driver.Queryer)(nil)).Elem(), //nolint:staticcheck // deprecated, and database/sql still type-asserts for it at sql.go:1780
		"ExecerContext":      reflect.TypeOf((*driver.ExecerContext)(nil)).Elem(),
		"Execer":             reflect.TypeOf((*driver.Execer)(nil)).Elem(), //nolint:staticcheck // deprecated, and database/sql still type-asserts for it at sql.go:1708
		"Pinger":             reflect.TypeOf((*driver.Pinger)(nil)).Elem(),
		"SessionResetter":    reflect.TypeOf((*driver.SessionResetter)(nil)).Elem(),
		"Validator":          reflect.TypeOf((*driver.Validator)(nil)).Elem(),
		"NamedValueChecker":  reflect.TypeOf((*driver.NamedValueChecker)(nil)).Elem(),
	}
}

func stmtInterfaces() map[string]reflect.Type {
	return map[string]reflect.Type{
		"StmtExecContext":   reflect.TypeOf((*driver.StmtExecContext)(nil)).Elem(),
		"StmtQueryContext":  reflect.TypeOf((*driver.StmtQueryContext)(nil)).Elem(),
		"NamedValueChecker": reflect.TypeOf((*driver.NamedValueChecker)(nil)).Elem(),
		"ColumnConverter":   reflect.TypeOf((*driver.ColumnConverter)(nil)).Elem(), //nolint:staticcheck // deprecated, and database/sql still type-asserts for it
	}
}

func rowsInterfaces() map[string]reflect.Type {
	return map[string]reflect.Type{
		"RowsColumnTypeScanType":         reflect.TypeOf((*driver.RowsColumnTypeScanType)(nil)).Elem(),
		"RowsColumnTypeDatabaseTypeName": reflect.TypeOf((*driver.RowsColumnTypeDatabaseTypeName)(nil)).Elem(),
		"RowsColumnTypeNullable":         reflect.TypeOf((*driver.RowsColumnTypeNullable)(nil)).Elem(),
		"RowsColumnTypeLength":           reflect.TypeOf((*driver.RowsColumnTypeLength)(nil)).Elem(),
		"RowsColumnTypePrecisionScale":   reflect.TypeOf((*driver.RowsColumnTypePrecisionScale)(nil)).Elem(),
		"RowsNextResultSet":              reflect.TypeOf((*driver.RowsNextResultSet)(nil)).Elem(),
		// driver.RowsColumnScanner is deliberately absent. It requires go1.27
		// and this module's floor is go1.23, so naming it here breaks the 1.23
		// leg of CI. The compiler said so, which is the only reason this is
		// known: nothing in the interface's own documentation announces a
		// minimum version.
		//
		// It is the one optional rows interface this check cannot guard, and
		// that gap is stated rather than left to be discovered. Add it when the
		// floor moves.
	}
}

// errBase is what the base driver returns when it is made to fail. It is not
// the injected error, so a test can tell the two apart.
var errBase = errors.New("the base driver refused")

// A failure from the BASE driver is not an injected failure. It passes through
// unchanged, and it counts no connection, because nothing was handed out.
//
// The mutation gate found this gap rather than a reader: deleting
// `return nil, err` from Connect changed no test result, because the test
// driver could not fail.
func TestABaseConnectFailurePassesThroughAndCountsNothing(t *testing.T) {
	base := &testDriver{connectErr: errBase}
	f := faultsql.New(&fault.Points{}, base)

	c, err := f.Connect(context.Background())
	if !errors.Is(err, errBase) {
		t.Errorf("Connect() = %v, want the base driver's own error unchanged", err)
	}
	if errors.Is(err, faultsql.ErrInjected) {
		t.Error("a base failure was reported as an injected failure, so a real defect would read as one this package caused")
	}
	if c != nil {
		t.Error("Connect returned a connection along with an error")
	}
	if got := f.Outstanding(); got != 0 {
		t.Errorf("Outstanding() = %d after a failed base connect, want 0", got)
	}
}

// Prepare is one operation and it fails at its armed point. Without this, a
// tripped Prepare could return no error at all and every test still passed.
func TestPrepareFailsAtTheArmedOperation(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{})

		c, err := f.Connect(context.Background()) // operation 1
		if err != nil {
			continue
		}
		st, prepErr := c.Prepare("select 1") // operation 2
		if st != nil {
			_ = st.Close()
		}
		_ = c.Close()

		if n != 2 {
			continue
		}
		reached = true
		if !errors.Is(prepErr, faultsql.ErrInjected) {
			t.Errorf("Prepare() = %v, want the injected error", prepErr)
		}
		if st != nil {
			t.Error("Prepare returned a statement along with an error")
		}
	}
	if !reached {
		t.Fatal("the sweep never armed the prepare, so nothing above was asserted")
	}
}

// Begin is one operation and it fails at its armed point.
//
// driver.Conn requires Begin, so it cannot wait for the transaction work in
// task 9. It trips rather than delegating silently, because an operation that
// reached the driver uncounted would shift every later index by one.
func TestBeginFailsAtTheArmedOperation(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{})

		c, err := f.Connect(context.Background()) // operation 1
		if err != nil {
			continue
		}
		tx, beginErr := c.Begin() //nolint:staticcheck // driver.Conn requires Begin, so the wrapper is tested through it
		_ = c.Close()

		if n != 2 {
			continue
		}
		reached = true
		if !errors.Is(beginErr, faultsql.ErrInjected) {
			t.Errorf("Begin() = %v, want the injected error", beginErr)
		}
		if tx != nil {
			t.Error("Begin returned a transaction along with an error")
		}
	}
	if !reached {
		t.Fatal("the sweep never armed the begin, so nothing above was asserted")
	}
}

// THE OTHER HALF OF FORK 2. TestTwoLiveConnectionsAreRefused proves the guard
// fires; this proves OpenDB does not rely on it, by setting the limit that
// makes two live connections impossible in the first place.
//
// The mutation gate found this gap too. SetMaxOpenConns(1) could become
// SetMaxOpenConns(0), which means UNLIMITED, and no test noticed.
func TestOpenDBLimitsThePoolToOneConnection(t *testing.T) {
	db, _ := faultsql.OpenDB(&fault.Points{}, &testDriver{})
	defer func() { _ = db.Close() }()

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1 — 0 means unlimited, which is the value that makes the operation index meaningless", got)
	}
}

// A statement's operations continue the same count the connection started.
// Exec, Query and Close each trip; NumInput does not.
func TestAStatementsOperationsEachCount(t *testing.T) {
	// operation 1 connect, 2 prepare, 3 exec, 4 query, 5 stmt close
	for _, op := range []struct {
		n    int
		name string
	}{{3, "Exec"}, {4, "Query"}, {5, "Close"}} {
		t.Run(op.name, func(t *testing.T) {
			reached := false
			for n, p := range fault.Sweep(t) {
				f := faultsql.New(p, &testDriver{})
				c, err := f.Connect(context.Background())
				if err != nil {
					continue
				}
				st, err := c.Prepare("select 1")
				if err != nil {
					_ = c.Close()
					continue
				}

				_, execErr := st.Exec(nil)   //nolint:staticcheck // driver.Stmt requires Exec, so the wrapper is tested through it
				_, queryErr := st.Query(nil) //nolint:staticcheck // driver.Stmt requires Query, so the wrapper is tested through it
				closeErr := st.Close()
				_ = c.Close()

				if n != op.n {
					continue
				}
				reached = true
				errs := map[string]error{"Exec": execErr, "Query": queryErr, "Close": closeErr}
				if got := errs[op.name]; !errors.Is(got, faultsql.ErrInjected) {
					t.Errorf("%s() = %v at operation %d, want the injected error", op.name, got, op.n)
				}
			}
			if !reached {
				t.Fatalf("the sweep never armed operation %d, so nothing was asserted", op.n)
			}
		})
	}
}

// NumInput is a property of the statement and not an operation on the
// database. Counting it would insert an index between the prepare and the
// exec, and every armed point after it would move.
//
// The assertion is positional rather than a count: with operation 3 armed, the
// Exec must fail. If NumInput counted, operation 3 would be one of the hundred
// NumInput calls and the Exec would succeed.
func TestNumInputDoesNotCount(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{})
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		st, err := c.Prepare("select 1") // 2
		if err != nil {
			_ = c.Close()
			continue
		}
		for range 100 {
			_ = st.NumInput() // must be none
		}
		_, execErr := st.Exec(nil) //nolint:staticcheck // driver.Stmt requires Exec, so the wrapper is tested through it
		_ = st.Close()
		_ = c.Close()

		if n != 3 {
			continue
		}
		reached = true
		if !errors.Is(execErr, faultsql.ErrInjected) {
			t.Errorf("Exec() = %v with operation 3 armed, want the injected error — a hundred NumInput calls moved the index", execErr)
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 3, so nothing was asserted")
	}
}

// Commit and Rollback each count. A caller that ignores the error from a
// Commit believes its data is durable when it is not, and that is the sharpest
// defect this adapter can inject.
func TestATransactionsOperationsEachCount(t *testing.T) {
	for _, name := range []string{"Commit", "Rollback"} {
		t.Run(name, func(t *testing.T) {
			reached := false
			for n, p := range fault.Sweep(t) {
				f := faultsql.New(p, &testDriver{})
				c, err := f.Connect(context.Background()) // 1
				if err != nil {
					continue
				}
				x, err := c.Begin() //nolint:staticcheck // driver.Conn requires Begin, so the wrapper is tested through it
				if err != nil {
					_ = c.Close()
					continue
				}

				var got error
				if name == "Commit" {
					got = x.Commit() // 3
				} else {
					got = x.Rollback() // 3
				}
				_ = c.Close()

				if n != 3 {
					continue
				}
				reached = true
				if !errors.Is(got, faultsql.ErrInjected) {
					t.Errorf("%s() = %v with operation 3 armed, want the injected error", name, got)
				}
			}
			if !reached {
				t.Fatal("the sweep never armed operation 3, so nothing was asserted")
			}
		})
	}
}

// THE ONE DELIBERATE EXCEPTION, asserted so it cannot become an accident.
//
// TWO REASONS, and the second is the one that settles it.
//
// The pool calls ResetSession, not the caller, and decides when from its own
// state, so counting it would make the N-th operation a different operation
// between two runs of one scenario. That is the smaller problem.
//
// The larger one is that there is no caller-visible error to inject at all.
// sql.go:1353 discards any error from resetSession that is not
// driver.ErrBadConn and hands the connection over anyway, and ErrInjected is
// deliberately not ErrBadConn. So counting it would consume an operation
// index, inject an error nobody ever sees, and let the pass run to completion
// reporting a pass -- the shape Fault.Err refuses for a short result set,
// manufactured on every sweep of every scenario.
//
// It must still be FORWARDED. Omitting the method would stop database/sql
// calling the base driver's reset at all, so the driver under test would keep
// state across pooled uses that it does not keep when it runs unwrapped.
func TestResetSessionIsForwardedAndDoesNotCount(t *testing.T) {
	base := &testDriver{}
	f := faultsql.New(&fault.Points{}, base)

	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	r, ok := c.(driver.SessionResetter)
	if !ok {
		t.Fatal("the wrapper does not implement driver.SessionResetter, so database/sql would never reset the base driver's session")
	}
	for range 5 {
		if err := r.ResetSession(context.Background()); err != nil {
			t.Fatalf("ResetSession: %v", err)
		}
	}
	_ = c.Close()

	if got := base.resets(); got != 5 {
		t.Errorf("the base saw %d reset(s), want 5 — the wrapper must forward it, not swallow it", got)
	}

	// Positional again: with operation 2 armed, the Close must fail. Five
	// resets in between must not take an index.
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{})
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		if r, ok := c.(driver.SessionResetter); ok {
			for range 5 {
				_ = r.ResetSession(context.Background()) // must be none
			}
		}
		closeErr := c.Close() // 2

		if n != 2 {
			continue
		}
		reached = true
		if !errors.Is(closeErr, faultsql.ErrInjected) {
			t.Errorf("Close() = %v with operation 2 armed, want the injected error — five resets moved the index", closeErr)
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 2, so nothing was asserted")
	}
}

// PrepareContext, BeginTx and Ping are operations, and each counts.
//
// The mutation gate found all three unreachable: no test drove any of them, so
// deleting the `return nil, ErrInjected` from each changed no result.
func TestTheContextMethodsEachCount(t *testing.T) {
	for _, c := range []struct {
		name string
		call func(*testing.T, driver.Conn) error
	}{
		{"PrepareContext", func(t *testing.T, c driver.Conn) error {
			st, err := asPrepareContext(t, c).PrepareContext(context.Background(), "select 1")
			if st != nil {
				_ = st.Close()
			}
			return err
		}},
		{"BeginTx", func(t *testing.T, c driver.Conn) error {
			_, err := asBeginTx(t, c).BeginTx(context.Background(), driver.TxOptions{})
			return err
		}},
		{"Ping", func(t *testing.T, c driver.Conn) error {
			return asPinger(t, c).Ping(context.Background())
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			reached := false
			for n, p := range fault.Sweep(t) {
				f := faultsql.New(p, &testDriver{})
				conn, err := f.Connect(context.Background()) // 1
				if err != nil {
					continue
				}
				got := c.call(t, conn) // 2
				_ = conn.Close()

				if n != 2 {
					continue
				}
				reached = true
				if !errors.Is(got, faultsql.ErrInjected) {
					t.Errorf("%s = %v with operation 2 armed, want the injected error", c.name, got)
				}
			}
			if !reached {
				t.Fatalf("the sweep never armed operation 2, so %s was not asserted", c.name)
			}
		})
	}
}

// A base driver that implements only the REQUIRED interfaces still works, and
// the wrapper's fallbacks are what make that true.
//
// database/sql is written for this driver as much as for a complete one. The
// mutation gate found every `if !ok` branch unreachable, because the only test
// driver implemented all four optional interfaces — the "testing a target that
// resembles the gate's target" defect, in the test data rather than the code.
func TestTheFallbacksWorkWhenTheBaseOffersOnlyTheRequiredMethods(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &plainDriver{})

	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	st, err := asPrepareContext(t, c).PrepareContext(context.Background(), "select 1")
	if err != nil {
		t.Errorf("PrepareContext against a base without it: %v", err)
	} else {
		_ = st.Close()
	}

	if _, err := asBeginTx(t, c).BeginTx(context.Background(), driver.TxOptions{}); err != nil {
		t.Errorf("BeginTx against a base without it: %v", err)
	}

	// A base that cannot be pinged reports success rather than an error. The
	// wrapper must not invent a failure the driver beneath it never had.
	if err := asPinger(t, c).Ping(context.Background()); err != nil {
		t.Errorf("Ping against a base without a Pinger = %v, want nil", err)
	}

	// The same for the session reset: nothing to reset is not an error.
	if err := asResetter(t, c).ResetSession(context.Background()); err != nil {
		t.Errorf("ResetSession against a base without it = %v, want nil", err)
	}
}

// A failure from the BASE passes through unchanged and is not reported as an
// injected one. Otherwise a real defect in the driver under test reads as a
// fault this package caused.
//
// The last two rows drive the FALLBACK paths: a base that offers neither
// ConnPrepareContext nor ConnBeginTx, and fails. Without them the wrapper can
// take its `if !ok` branch and never see it fail, which is what the mutation
// gate reported after the first repair — the branch was reachable, and the
// error inside it was not.
func TestABaseFailurePassesThroughUnchanged(t *testing.T) {
	for _, c := range []struct {
		name string
		base driver.Connector
		call func(*testing.T, driver.Conn) error
	}{
		{"Prepare", &testDriver{prepareErr: errBase}, func(t *testing.T, c driver.Conn) error {
			_, err := c.Prepare("select 1")
			return err
		}},
		{"PrepareContext", &testDriver{prepareErr: errBase}, func(t *testing.T, c driver.Conn) error {
			_, err := asPrepareContext(t, c).PrepareContext(context.Background(), "select 1")
			return err
		}},
		{"Begin", &testDriver{beginErr: errBase}, func(t *testing.T, c driver.Conn) error {
			_, err := c.Begin() //nolint:staticcheck // driver.Conn requires Begin, so the wrapper is tested through it
			return err
		}},
		{"BeginTx", &testDriver{beginErr: errBase}, func(t *testing.T, c driver.Conn) error {
			_, err := asBeginTx(t, c).BeginTx(context.Background(), driver.TxOptions{})
			return err
		}},
		{"PrepareContext falling back", &plainDriver{prepareErr: errBase}, func(t *testing.T, c driver.Conn) error {
			_, err := asPrepareContext(t, c).PrepareContext(context.Background(), "select 1")
			return err
		}},
		{"BeginTx falling back", &plainDriver{beginErr: errBase}, func(t *testing.T, c driver.Conn) error {
			_, err := asBeginTx(t, c).BeginTx(context.Background(), driver.TxOptions{})
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := faultsql.New(&fault.Points{}, c.base)
			conn, err := f.Connect(context.Background())
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer func() { _ = conn.Close() }()

			got := c.call(t, conn)
			if !errors.Is(got, errBase) {
				t.Errorf("%s = %v, want the base driver's own error unchanged", c.name, got)
			}
			if errors.Is(got, faultsql.ErrInjected) {
				t.Errorf("%s reported a base failure as an injected one, so a real defect would read as one this package caused", c.name)
			}
		})
	}
}

// The three helpers below exist because .golangci.yml sets
// errcheck.check-type-assertions, so a one-value type assertion is a finding.
// That strictness is right here rather than merely satisfied: the two-value
// form turns "the wrapper does not implement this" from a panic into a
// sentence, and that sentence is the finding fork 3 is about.

func asPrepareContext(t *testing.T, c driver.Conn) driver.ConnPrepareContext {
	t.Helper()
	v, ok := c.(driver.ConnPrepareContext)
	if !ok {
		t.Fatal("the wrapper does not implement driver.ConnPrepareContext, so database/sql would drop the caller's context")
	}
	return v
}

func asBeginTx(t *testing.T, c driver.Conn) driver.ConnBeginTx {
	t.Helper()
	v, ok := c.(driver.ConnBeginTx)
	if !ok {
		t.Fatal("the wrapper does not implement driver.ConnBeginTx")
	}
	return v
}

func asPinger(t *testing.T, c driver.Conn) driver.Pinger {
	t.Helper()
	v, ok := c.(driver.Pinger)
	if !ok {
		t.Fatal("the wrapper does not implement driver.Pinger, so db.Ping would report success against a driver whose Ping fails")
	}
	return v
}

func asResetter(t *testing.T, c driver.Conn) driver.SessionResetter {
	t.Helper()
	v, ok := c.(driver.SessionResetter)
	if !ok {
		t.Fatal("the wrapper does not implement driver.SessionResetter, so database/sql would never reset the base driver's session")
	}
	return v
}

// THE ANSWER TO FORK 4, and the property everything else rests on.
//
// Next consumes exactly ONE operation index per result set, on its first call.
// It does not consume one per row. Per row was the obvious reading and it is
// wrong twice: a 600-row query would pass the core's maxOps of 512 and the
// sweep would report non-termination, which reads exactly like a defect in the
// code under test; and the count would follow the DATA, so adding a row to a
// fixture would move every later armed point.
//
// THE ASSERTION IS ABOUT A LATER OPERATION, and it has to be. An earlier
// version of this test armed the Next itself and checked that it failed, which
// per-row counting satisfies just as well: with the failure delivered at row 0
// the two rules are indistinguishable there. It passed against a deliberately
// per-row implementation, so it asserted nothing about the property it named.
//
// The difference only shows up AFTER the drain. The scenario is five
// operations regardless of how many rows arrive:
//
//	1 connect   2 query   3 the whole drain   4 rows.Close   5 conn.Close
//
// Under per-row counting a ten-row result set would put conn.Close at 13.
func TestNextCountsOncePerResultSetAndNotOncePerRow(t *testing.T) {
	for _, rowCount := range []int{2, 10} {
		t.Run(fmt.Sprintf("%drows", rowCount), func(t *testing.T) {
			reached := false
			for n, p := range fault.Sweep(t) {
				f := faultsql.New(p, &testDriver{rows: rowCount})
				c, err := f.Connect(context.Background()) // 1
				if err != nil {
					continue
				}
				q, ok := c.(driver.QueryerContext)
				if !ok {
					t.Fatal("the wrapper does not implement driver.QueryerContext")
				}
				rs, err := q.QueryContext(context.Background(), "select n", nil) // 2
				if err != nil {
					_ = c.Close()
					continue
				}
				for { // 3, once for the whole drain
					if e := rs.Next(make([]driver.Value, 1)); e != nil {
						break
					}
				}
				_ = rs.Close()        // 4
				closeErr := c.Close() // 5

				if n != 5 {
					continue
				}
				reached = true
				if !errors.Is(closeErr, faultsql.ErrInjected) {
					t.Errorf("conn.Close() = %v with operation 5 armed and %d rows, want the injected error — the drain consumed more than one index, so the count follows the data", closeErr, rowCount)
				}
			}
			if !reached {
				t.Fatalf("the sweep never armed operation 5 with %d rows, so nothing was asserted — under per-row counting it would not reach it", rowCount)
			}
		})
	}
}

// Columns is a property of the result set, not an operation, in the same way
// NumInput is for a statement.
func TestColumnsDoesNotCount(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{rows: 3})
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		q, ok := c.(driver.QueryerContext)
		if !ok {
			t.Fatal("the wrapper does not implement driver.QueryerContext")
		}
		rs, err := q.QueryContext(context.Background(), "select n", nil) // 2
		if err != nil {
			_ = c.Close()
			continue
		}
		for range 100 {
			_ = rs.Columns() // must be none
		}
		nextErr := rs.Next(make([]driver.Value, 1)) // 3
		_ = rs.Close()
		_ = c.Close()

		if n != 3 {
			continue
		}
		reached = true
		if !errors.Is(nextErr, faultsql.ErrInjected) {
			t.Errorf("Next() = %v with operation 3 armed, want the injected error — a hundred Columns calls moved the index", nextErr)
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 3, so nothing was asserted")
	}
}

// NewAtRow moves WHERE the failure appears, and never the count.
func TestNewAtRowDeliversAtTheNamedRow(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.NewAtRow(p, &testDriver{rows: 5}, 3)
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		q, ok := c.(driver.QueryerContext)
		if !ok {
			t.Fatal("the wrapper does not implement driver.QueryerContext")
		}
		rs, err := q.QueryContext(context.Background(), "select n", nil) // 2
		if err != nil {
			_ = c.Close()
			continue
		}

		delivered, seen := -1, 0
		for {
			e := rs.Next(make([]driver.Value, 1)) // 3, once for the whole drain
			if errors.Is(e, faultsql.ErrInjected) {
				delivered = seen
				break
			}
			if e != nil {
				break
			}
			seen++
		}
		_ = rs.Close()
		_ = c.Close()

		if n != 3 {
			continue
		}
		reached = true
		if delivered != 3 {
			t.Errorf("the failure arrived after %d rows, want 3 — NewAtRow names where it appears", delivered)
		}
		if err := f.Err(); err != nil {
			t.Errorf("a result set long enough for the named row was refused: %v", err)
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 3, so nothing was asserted")
	}
}

// A named row past the end of the result set consumed an operation index and
// injected nothing. That is not a pass, and Err refuses it rather than letting
// it read as one.
//
// This is the case that made the design document's original wording wrong. It
// said "one trip per result set, at a row the caller names", and named a row
// the data might never reach.
func TestANamedRowPastTheEndIsRefused(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.NewAtRow(p, &testDriver{rows: 2}, 400)
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		q, ok := c.(driver.QueryerContext)
		if !ok {
			t.Fatal("the wrapper does not implement driver.QueryerContext")
		}
		rs, err := q.QueryContext(context.Background(), "select n", nil) // 2
		if err != nil {
			_ = c.Close()
			continue
		}
		for {
			if e := rs.Next(make([]driver.Value, 1)); e != nil { // 3
				break
			}
		}
		_ = rs.Close()
		_ = c.Close()

		if n != 3 {
			continue
		}
		reached = true
		if f.Err() == nil {
			t.Error("the pass consumed an operation index and injected nothing, and it was not refused — a sweep that asserts nothing must never report a pass")
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 3, so nothing was asserted")
	}
}

// QueryContext and ExecContext are operations, and each counts.
func TestTheQueryAndExecContextMethodsEachCount(t *testing.T) {
	for _, c := range []struct {
		name string
		call func(*testing.T, driver.Conn) error
	}{
		{"QueryContext", func(t *testing.T, c driver.Conn) error {
			q, ok := c.(driver.QueryerContext)
			if !ok {
				t.Fatal("the wrapper does not implement driver.QueryerContext")
			}
			rs, err := q.QueryContext(context.Background(), "select n", nil)
			if rs != nil {
				_ = rs.Close()
			}
			return err
		}},
		{"ExecContext", func(t *testing.T, c driver.Conn) error {
			e, ok := c.(driver.ExecerContext)
			if !ok {
				t.Fatal("the wrapper does not implement driver.ExecerContext")
			}
			_, err := e.ExecContext(context.Background(), "update t", nil)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			reached := false
			for n, p := range fault.Sweep(t) {
				f := faultsql.New(p, &testDriver{})
				conn, err := f.Connect(context.Background()) // 1
				if err != nil {
					continue
				}
				got := c.call(t, conn) // 2
				_ = conn.Close()

				if n != 2 {
					continue
				}
				reached = true
				if !errors.Is(got, faultsql.ErrInjected) {
					t.Errorf("%s = %v with operation 2 armed, want the injected error", c.name, got)
				}
			}
			if !reached {
				t.Fatalf("the sweep never armed operation 2, so %s was not asserted", c.name)
			}
		})
	}
}

// A base that implements neither QueryerContext nor ExecerContext gets
// driver.ErrSkip, which is the documented way to say "continue as if
// unimplemented" (driver.go:150).
//
// It must consume NO operation index, because no operation happened. Returning
// the injected error instead would fail a query the base could have served
// through the prepare-then-query path.
func TestErrSkipWhenTheBaseImplementsNeither(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &plainDriver{})
	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	q, ok := c.(driver.QueryerContext)
	if !ok {
		t.Fatal("the wrapper does not implement driver.QueryerContext")
	}
	if _, err := q.QueryContext(context.Background(), "select n", nil); !errors.Is(err, driver.ErrSkip) {
		t.Errorf("QueryContext against a base without it = %v, want driver.ErrSkip", err)
	}

	e, ok := c.(driver.ExecerContext)
	if !ok {
		t.Fatal("the wrapper does not implement driver.ExecerContext")
	}
	if _, err := e.ExecContext(context.Background(), "update t", nil); !errors.Is(err, driver.ErrSkip) {
		t.Errorf("ExecContext against a base without it = %v, want driver.ErrSkip", err)
	}
}

// A base failure from a query or an exec passes through unchanged.
func TestABaseQueryFailurePassesThroughUnchanged(t *testing.T) {
	for _, c := range []struct {
		name string
		base *testDriver
		call func(*testing.T, driver.Conn) error
	}{
		{"QueryContext", &testDriver{queryErr: errBase}, func(t *testing.T, c driver.Conn) error {
			q, ok := c.(driver.QueryerContext)
			if !ok {
				t.Fatal("the wrapper does not implement driver.QueryerContext")
			}
			_, err := q.QueryContext(context.Background(), "select n", nil)
			return err
		}},
		{"ExecContext", &testDriver{execErr: errBase}, func(t *testing.T, c driver.Conn) error {
			e, ok := c.(driver.ExecerContext)
			if !ok {
				t.Fatal("the wrapper does not implement driver.ExecerContext")
			}
			_, err := e.ExecContext(context.Background(), "update t", nil)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := faultsql.New(&fault.Points{}, c.base)
			conn, err := f.Connect(context.Background())
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer func() { _ = conn.Close() }()

			if got := c.call(t, conn); !errors.Is(got, errBase) {
				t.Errorf("%s = %v, want the base driver's own error unchanged", c.name, got)
			}
		})
	}
}

// rows.Close is an operation, and it closes the real result set whether or not
// it trips.
func TestRowsCloseCounts(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{rows: 2})
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		q, ok := c.(driver.QueryerContext)
		if !ok {
			t.Fatal("the wrapper does not implement driver.QueryerContext")
		}
		rs, err := q.QueryContext(context.Background(), "select n", nil) // 2
		if err != nil {
			_ = c.Close()
			continue
		}
		for { // 3
			if e := rs.Next(make([]driver.Value, 1)); e != nil {
				break
			}
		}
		closeErr := rs.Close() // 4
		_ = c.Close()

		if n != 4 {
			continue
		}
		reached = true
		if !errors.Is(closeErr, faultsql.ErrInjected) {
			t.Errorf("rows.Close() = %v with operation 4 armed, want the injected error", closeErr)
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 4, so nothing was asserted")
	}
}

// A pass that arms NOTHING in the result set must deliver every row.
//
// The mutation gate found this gap by turning `if r.armed && r.n == r.at` into
// `if true && r.n == r.at`, which fails the first row of every pass, and no
// test noticed. A fault injector that fails an operation it was not asked to
// fail reports defects the code under test does not have, and every later
// finding then needs a person to re-derive whether it is real.
func TestAnUnarmedResultSetDeliversEveryRow(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &testDriver{rows: 7})
	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	q, ok := c.(driver.QueryerContext)
	if !ok {
		t.Fatal("the wrapper does not implement driver.QueryerContext")
	}
	rs, err := q.QueryContext(context.Background(), "select n", nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rs.Close() }()

	seen := 0
	for {
		e := rs.Next(make([]driver.Value, 1))
		if errors.Is(e, faultsql.ErrInjected) {
			t.Fatalf("row %d failed with the injected error, and a zero Points arms nothing", seen)
		}
		if e != nil {
			break
		}
		seen++
	}
	if seen != 7 {
		t.Errorf("the drain saw %d rows, want 7", seen)
	}
}

// A base failure from a prepared statement's Query passes through unchanged.
//
// This is a different path from conn.QueryContext: stmt.Query wraps the result
// set, so its error branch is its own. The mutation gate reported it
// unreachable after the field existed but nothing set it, which is the
// difference between adding a fixture knob and turning it.
func TestABaseStatementQueryFailurePassesThroughUnchanged(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &testDriver{stmtQueryErr: errBase})
	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	st, err := c.Prepare("select n")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = st.Close() }()

	rs, got := st.Query(nil) //nolint:staticcheck // driver.Stmt requires Query, so the wrapper is tested through it
	if rs != nil {
		_ = rs.Close()
	}
	if !errors.Is(got, errBase) {
		t.Errorf("stmt.Query = %v, want the base driver's own error unchanged", got)
	}
	if errors.Is(got, faultsql.ErrInjected) {
		t.Error("a base failure was reported as an injected one")
	}
}

// THE BEHAVIOUR THE STATEMENT-LEVEL GAP REMOVED, asserted directly.
//
// ctxutil.go:81 calls si.Query(dargs) when a Stmt has no StmtQueryContext, and
// the context is discarded on the way. A caller's cancellation then stops
// working the moment the driver is wrapped, and nothing reports it: the call
// count does not change and no error appears.
//
// This test does not read the interface list. It cancels a context and asks
// the base driver what it saw.
func TestACancelledContextReachesTheBaseThroughTheWrapper(t *testing.T) {
	base := &testDriver{}
	f := faultsql.New(&fault.Points{}, base)

	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	st, err := c.Prepare("select n")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = st.Close() }()

	q, ok := st.(driver.StmtQueryContext)
	if !ok {
		t.Fatal("the wrapped statement does not implement driver.StmtQueryContext, so database/sql would drop the caller's context")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rs, err := q.QueryContext(ctx, nil)
	if rs != nil {
		_ = rs.Close()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("QueryContext: %v", err)
	}

	// The base saw the cancellation. Without StmtQueryContext on the wrapper
	// this is nil, because the context never travelled.
	if got := base.stmtCtxErr(); !errors.Is(got, context.Canceled) {
		t.Errorf("the base driver saw ctx.Err() = %v, want context.Canceled — the wrapper dropped the context", got)
	}
}

// The statement's context methods REPLACE Exec and Query rather than adding a
// call, so each consumes exactly one operation index.
//
// Two would be worse than none: wrapping a driver would move every armed point
// after the first statement, and the sweep would be measuring the adapter.
func TestTheStatementContextMethodsCountOnce(t *testing.T) {
	// closeIdx differs between the two, and stating it beats assuming it.
	// QueryContext hands back a result set, and closing that set is itself an
	// operation, so the connection close lands one later. The first version of
	// this test wrote 5 for both and failed -- correctly.
	for _, c := range []struct {
		name     string
		closeIdx int
		call     func(*testing.T, driver.Stmt) error
	}{
		{"ExecContext", 5, func(t *testing.T, st driver.Stmt) error {
			e, ok := st.(driver.StmtExecContext)
			if !ok {
				t.Fatal("no driver.StmtExecContext on the wrapped statement")
			}
			_, err := e.ExecContext(context.Background(), nil)
			return err
		}},
		{"QueryContext", 6, func(t *testing.T, st driver.Stmt) error {
			q, ok := st.(driver.StmtQueryContext)
			if !ok {
				t.Fatal("no driver.StmtQueryContext on the wrapped statement")
			}
			rs, err := q.QueryContext(context.Background(), nil)
			if rs != nil {
				_ = rs.Close()
			}
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			reached := false
			for n, p := range fault.Sweep(t) {
				f := faultsql.New(p, &testDriver{})
				conn, err := f.Connect(context.Background()) // 1
				if err != nil {
					continue
				}
				st, err := conn.Prepare("select n") // 2
				if err != nil {
					_ = conn.Close()
					continue
				}
				got := c.call(t, st) // 3, and for QueryContext the rows close is 4
				_ = st.Close()
				closeErr := conn.Close()

				switch n {
				case 3:
					reached = true
					if !errors.Is(got, faultsql.ErrInjected) {
						t.Errorf("%s = %v with operation 3 armed, want the injected error", c.name, got)
					}
				case c.closeIdx:
					// The count did not move. If the context method consumed
					// two indexes, the connection close would sit one later.
					if !errors.Is(closeErr, faultsql.ErrInjected) {
						t.Errorf("conn.Close() = %v with operation %d armed, want the injected error — %s consumed more than one index",
							closeErr, c.closeIdx, c.name)
					}
				}
			}
			if !reached {
				t.Fatalf("the sweep never armed operation 3, so %s was not asserted", c.name)
			}
		})
	}
}

// The column-type methods describe the shape of the result set. They are
// forwarded, and none of them counts.
//
// Counting them would make the operation index depend on how often a caller
// asked about the schema, which is not work the database performs. It is the
// same rule Columns and NumInput follow.
func TestTheColumnTypeMethodsAreForwardedAndDoNotCount(t *testing.T) {
	base := &testDriver{rows: 2}
	f := faultsql.New(&fault.Points{}, base)
	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	rs := queryRows(t, c)
	defer func() { _ = rs.Close() }()

	st, ok := rs.(driver.RowsColumnTypeScanType)
	if !ok {
		t.Fatal("the wrapped rows do not implement driver.RowsColumnTypeScanType")
	}
	if got := st.ColumnTypeScanType(0); got != reflect.TypeOf(int64(0)) {
		t.Errorf("ColumnTypeScanType(0) = %v, want int64 — the wrapper is not forwarding", got)
	}
	dt, ok := rs.(driver.RowsColumnTypeDatabaseTypeName)
	if !ok {
		t.Fatal("the wrapped rows do not implement driver.RowsColumnTypeDatabaseTypeName")
	}
	if got := dt.ColumnTypeDatabaseTypeName(0); got != "BIGINT" {
		t.Errorf("ColumnTypeDatabaseTypeName(0) = %q, want BIGINT", got)
	}

	// The forwarding half is done. The "does not count" half needs a SWEEP,
	// and the first version of this test did not have one: it asked the
	// questions under a zero Points, where Trip never fires, so adding a count
	// to ColumnTypeScanType changed nothing observable and the control did not
	// fire. A no-count claim is only testable where something is armed.
	assertSchemaQuestionsTakeNoIndex(t)
}

// assertSchemaQuestionsTakeNoIndex arms operation 3 and requires the Next to
// fail. A hundred schema questions sit between the query and the Next, so if
// any of them consumed an index, operation 3 would be one of THEM and the Next
// would succeed.
func assertSchemaQuestionsTakeNoIndex(t *testing.T) {
	t.Helper()
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{rows: 2})
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		rs, err := queryRowsErr(c) // 2
		if err != nil {
			_ = c.Close()
			continue
		}
		for range 100 { // must take none
			if v, ok := rs.(driver.RowsColumnTypeScanType); ok {
				_ = v.ColumnTypeScanType(0)
			}
			if v, ok := rs.(driver.RowsColumnTypeDatabaseTypeName); ok {
				_ = v.ColumnTypeDatabaseTypeName(0)
			}
			if v, ok := rs.(driver.RowsColumnTypeNullable); ok {
				_, _ = v.ColumnTypeNullable(0)
			}
			if v, ok := rs.(driver.RowsColumnTypeLength); ok {
				_, _ = v.ColumnTypeLength(0)
			}
			if v, ok := rs.(driver.RowsColumnTypePrecisionScale); ok {
				_, _, _ = v.ColumnTypePrecisionScale(0)
			}
			if v, ok := rs.(driver.RowsNextResultSet); ok {
				_ = v.HasNextResultSet()
			}
		}
		nextErr := rs.Next(make([]driver.Value, 1)) // 3
		_ = rs.Close()
		_ = c.Close()

		if n != 3 {
			continue
		}
		reached = true
		if !errors.Is(nextErr, faultsql.ErrInjected) {
			t.Errorf("Next() = %v with operation 3 armed, want the injected error — a schema question took an index", nextErr)
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 3, so nothing was asserted")
	}
}

// NextResultSet is the one member of that group that DOES count, and the new
// result set gets its own budget.
//
// It advances to another result set, which is work the database performs. The
// row rule is one index per result set, and a second result set is a second
// one.
func TestNextResultSetCountsAndResetsTheRowBudget(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.New(p, &testDriver{rows: 2})
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		rs, err := queryRowsErr(c) // 2
		if err != nil {
			_ = c.Close()
			continue
		}
		nrs, ok := rs.(driver.RowsNextResultSet)
		if !ok {
			t.Fatal("the wrapped rows do not implement driver.RowsNextResultSet")
		}
		nextErr := nrs.NextResultSet() // 3
		_ = rs.Close()
		_ = c.Close()

		if n != 3 {
			continue
		}
		reached = true
		if !errors.Is(nextErr, faultsql.ErrInjected) {
			t.Errorf("NextResultSet() = %v with operation 3 armed, want the injected error", nextErr)
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 3, so nothing was asserted")
	}
}

// A base that offers only the required methods gets driver.ErrSkip from the
// statement's context methods, and zero values from the column-type ones.
//
// ErrSkip is the documented way to say "continue as if unimplemented"
// (driver.go:150), so database/sql takes the path it would have taken without
// the method. No index is consumed, because no operation happened.
func TestTheStatementAndRowsFallbacksAgainstAPlainBase(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &plainDriver{})
	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	st, err := c.Prepare("select n")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = st.Close() }()

	e, ok := st.(driver.StmtExecContext)
	if !ok {
		t.Fatal("the wrapped statement does not implement driver.StmtExecContext")
	}
	if _, err := e.ExecContext(context.Background(), nil); !errors.Is(err, driver.ErrSkip) {
		t.Errorf("ExecContext against a plain base = %v, want driver.ErrSkip", err)
	}
	q, ok := st.(driver.StmtQueryContext)
	if !ok {
		t.Fatal("the wrapped statement does not implement driver.StmtQueryContext")
	}
	if _, err := q.QueryContext(context.Background(), nil); !errors.Is(err, driver.ErrSkip) {
		t.Errorf("QueryContext against a plain base = %v, want driver.ErrSkip", err)
	}

	// The rows side: a plain base gives zero values rather than a panic.
	rs, err := st.Query(nil) //nolint:staticcheck // driver.Stmt requires Query, so the wrapper is tested through it
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rs.Close() }()

	if v, ok := rs.(driver.RowsColumnTypeDatabaseTypeName); !ok {
		t.Error("the wrapped rows do not implement driver.RowsColumnTypeDatabaseTypeName")
	} else if got := v.ColumnTypeDatabaseTypeName(0); got != "" {
		t.Errorf("ColumnTypeDatabaseTypeName against a plain base = %q, want the empty string", got)
	}
	if v, ok := rs.(driver.RowsColumnTypeNullable); !ok {
		t.Error("the wrapped rows do not implement driver.RowsColumnTypeNullable")
	} else if _, known := v.ColumnTypeNullable(0); known {
		t.Error("ColumnTypeNullable against a plain base reported the answer as known")
	}
	if v, ok := rs.(driver.RowsColumnTypeLength); !ok {
		t.Error("the wrapped rows do not implement driver.RowsColumnTypeLength")
	} else if _, known := v.ColumnTypeLength(0); known {
		t.Error("ColumnTypeLength against a plain base reported the answer as known")
	}
	if v, ok := rs.(driver.RowsColumnTypePrecisionScale); !ok {
		t.Error("the wrapped rows do not implement driver.RowsColumnTypePrecisionScale")
	} else if _, _, known := v.ColumnTypePrecisionScale(0); known {
		t.Error("ColumnTypePrecisionScale against a plain base reported the answer as known")
	}
	if v, ok := rs.(driver.RowsColumnTypeScanType); !ok {
		t.Error("the wrapped rows do not implement driver.RowsColumnTypeScanType")
	} else if got := v.ColumnTypeScanType(0); got.Kind() != reflect.Interface {
		t.Errorf("ColumnTypeScanType against a plain base = %v, want the empty interface", got)
	}
	if v, ok := rs.(driver.RowsNextResultSet); !ok {
		t.Error("the wrapped rows do not implement driver.RowsNextResultSet")
	} else {
		if v.HasNextResultSet() {
			t.Error("HasNextResultSet against a plain base reported another result set")
		}
		if err := v.NextResultSet(); err == nil {
			t.Error("NextResultSet against a plain base returned nil, want an error")
		}
	}
}

func queryRowsErr(c driver.Conn) (driver.Rows, error) {
	q, ok := c.(driver.QueryerContext)
	if !ok {
		return nil, errBase
	}
	return q.QueryContext(context.Background(), "select n", nil)
}

// All FIVE column-type methods forward, not the two the first version checked.
//
// The mutation gate found the other three: replacing the body of
// ColumnTypeNullable, ColumnTypeLength or ColumnTypePrecisionScale with a
// discard changed no test result, because nothing read what they returned.
func TestEveryColumnTypeMethodForwardsItsValue(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &testDriver{rows: 2})
	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	rs := queryRows(t, c)
	defer func() { _ = rs.Close() }()

	if v, ok := rs.(driver.RowsColumnTypeNullable); !ok {
		t.Error("no driver.RowsColumnTypeNullable on the wrapped rows")
	} else if nullable, known := v.ColumnTypeNullable(0); nullable || !known {
		t.Errorf("ColumnTypeNullable(0) = (%v, %v), want (false, true) — the base says so", nullable, known)
	}
	if v, ok := rs.(driver.RowsColumnTypeLength); !ok {
		t.Error("no driver.RowsColumnTypeLength on the wrapped rows")
	} else if length, known := v.ColumnTypeLength(0); length != 42 || !known {
		t.Errorf("ColumnTypeLength(0) = (%d, %v), want (42, true) — the base says so, and 42 is chosen because it is NOT what the fallback returns", length, known)
	}
	if v, ok := rs.(driver.RowsColumnTypePrecisionScale); !ok {
		t.Error("no driver.RowsColumnTypePrecisionScale on the wrapped rows")
	} else if pr, sc, known := v.ColumnTypePrecisionScale(0); pr != 10 || sc != 2 || !known {
		t.Errorf("ColumnTypePrecisionScale(0) = (%d, %d, %v), want (10, 2, true) — distinct from the fallback's (0, 0, false)", pr, sc, known)
	}
}

// The zero values a plain base gets are ZERO, and the number matters as much
// as the boolean.
//
// The mutation gate found this by turning `return 0, false` into
// `return -1, false` and `return 1, false` with no test noticing. A caller
// that reads the length before checking ok gets a number either way, and -1 is
// a different lie from 0.
func TestThePlainBaseZeroValuesAreZero(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &plainDriver{})
	c, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = c.Close() }()

	st, err := c.Prepare("select n")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = st.Close() }()

	rs, err := st.Query(nil) //nolint:staticcheck // driver.Stmt requires Query, so the wrapper is tested through it
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer func() { _ = rs.Close() }()

	if v, ok := rs.(driver.RowsColumnTypeLength); !ok {
		t.Error("no driver.RowsColumnTypeLength on the wrapped rows")
	} else if length, known := v.ColumnTypeLength(0); length != 0 || known {
		t.Errorf("ColumnTypeLength against a plain base = (%d, %v), want (0, false)", length, known)
	}
	if v, ok := rs.(driver.RowsColumnTypePrecisionScale); !ok {
		t.Error("no driver.RowsColumnTypePrecisionScale on the wrapped rows")
	} else if pr, sc, known := v.ColumnTypePrecisionScale(0); pr != 0 || sc != 0 || known {
		t.Errorf("ColumnTypePrecisionScale against a plain base = (%d, %d, %v), want (0, 0, false)", pr, sc, known)
	}
}

// A base failure from the statement's QueryContext, and from NextResultSet,
// passes through unchanged.
func TestABaseFailureFromTheNewMethodsPassesThrough(t *testing.T) {
	t.Run("StmtQueryContext", func(t *testing.T) {
		f := faultsql.New(&fault.Points{}, &testDriver{stmtQueryErr: errBase})
		c, err := f.Connect(context.Background())
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() { _ = c.Close() }()

		st, err := c.Prepare("select n")
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		defer func() { _ = st.Close() }()

		q, ok := st.(driver.StmtQueryContext)
		if !ok {
			t.Fatal("no driver.StmtQueryContext on the wrapped statement")
		}
		if _, err := q.QueryContext(context.Background(), nil); !errors.Is(err, errBase) {
			t.Errorf("QueryContext = %v, want the base driver's own error unchanged", err)
		}
	})

	t.Run("NextResultSet", func(t *testing.T) {
		f := faultsql.New(&fault.Points{}, &testDriver{rows: 2, nextSetErr: errBase})
		c, err := f.Connect(context.Background())
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		defer func() { _ = c.Close() }()

		rs := queryRows(t, c)
		defer func() { _ = rs.Close() }()

		nrs, ok := rs.(driver.RowsNextResultSet)
		if !ok {
			t.Fatal("no driver.RowsNextResultSet on the wrapped rows")
		}
		if err := nrs.NextResultSet(); !errors.Is(err, errBase) {
			t.Errorf("NextResultSet = %v, want the base driver's own error unchanged", err)
		}
	})
}

// THE SECOND RESULT SET GETS ITS OWN ROW BUDGET, counted from zero.
//
// This is the assertion the reset line needs, and it is only observable when
// something is armed: r.n decides WHERE the failure lands, and with nothing
// armed nothing lands anywhere. The mutation gate found three mutants here —
// the reset to -1, the reset to 1, and the reset removed entirely — and none
// of them changed a test result.
//
// The scenario, with the row named as 1 so the position is not row 0 by
// default:
//
//	1 connect  2 query  3 the first set's drain  4 NextResultSet  5 the
//	second set's first Next
//
// With operation 5 armed, the failure must arrive after exactly ONE row of the
// SECOND set. If the counter were not reset it would still hold 2 from the
// first set, never reach 1, and the pass would consume an index and inject
// nothing — which Fault.Err refuses, so that case is caught too.
func TestTheSecondResultSetCountsItsRowsFromZero(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		f := faultsql.NewAtRow(p, &testDriver{rows: 2, moreRows: 4}, 1)
		c, err := f.Connect(context.Background()) // 1
		if err != nil {
			continue
		}
		rs, err := queryRowsErr(c) // 2
		if err != nil {
			_ = c.Close()
			continue
		}
		for { // 3
			if e := rs.Next(make([]driver.Value, 1)); e != nil {
				break
			}
		}
		nrs, ok := rs.(driver.RowsNextResultSet)
		if !ok {
			t.Fatal("no driver.RowsNextResultSet on the wrapped rows")
		}
		advanced := nrs.NextResultSet() // 4

		delivered, seen := -1, 0
		if advanced == nil {
			for { // 5, once for the whole second drain
				e := rs.Next(make([]driver.Value, 1))
				if errors.Is(e, faultsql.ErrInjected) {
					delivered = seen
					break
				}
				if e != nil {
					break
				}
				seen++
			}
		}
		refusal := f.Err()
		_ = rs.Close()
		_ = c.Close()

		if n != 5 {
			continue
		}
		reached = true
		if advanced != nil {
			t.Fatalf("NextResultSet did not advance: %v", advanced)
		}
		if delivered != 1 {
			t.Errorf("the failure arrived after %d row(s) of the second set, want 1 — the row counter was not reset to zero", delivered)
		}
		if refusal != nil {
			t.Errorf("the pass was refused: %v — the armed row was never reached in the second set", refusal)
		}
	}
	if !reached {
		t.Fatal("the sweep never armed operation 5, so nothing was asserted")
	}
}

// Connector bridges a driver.Driver to the driver.Connector this package takes.
//
// THE CASE IT EXISTS FOR is a driver that implements neither Connector nor
// DriverContext, because that is what a real one does. Measured on
// modernc.org/sqlite v1.57.0 in the spike beside this repository:
// Connector=false, DriverContext=false. Both in-module fixtures already
// implement Connect, so neither resembles the case, and openOnlyDriver is
// written to be the one that does.
func TestConnectorBridgesADriverThatOffersOnlyOpen(t *testing.T) {
	base := &openOnlyDriver{}
	c, err := faultsql.Connector(base, "file:spike.db")
	if err != nil {
		t.Fatalf("Connector: %v", err)
	}
	if got := c.Driver(); got != driver.Driver(base) {
		t.Errorf("Driver() = %v, want the driver it was given", got)
	}

	f := faultsql.New(&fault.Points{}, c)
	conn, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect through the bridge: %v", err)
	}
	defer func() { _ = conn.Close() }()

	opens, dsn := base.counts()
	if opens != 1 {
		t.Errorf("the driver saw %d Open(s), want 1", opens)
	}
	if dsn != "file:spike.db" {
		t.Errorf("the driver was given the DSN %q, want file:spike.db", dsn)
	}
}

// A driver that offers OpenConnector gets its own, not the DSN fallback.
//
// This follows sql.Open rather than inventing a rule. The connector a driver
// builds may hold parsed state the DSN string does not, and the fallback would
// re-parse the DSN on every connect instead.
func TestConnectorPrefersOpenConnectorWhenTheDriverHasIt(t *testing.T) {
	base := &ctxDriver{}
	c, err := faultsql.Connector(base, "file:spike.db")
	if err != nil {
		t.Fatalf("Connector: %v", err)
	}
	if got := base.connectors(); got != 1 {
		t.Errorf("OpenConnector was called %d time(s), want 1 — the bridge took the DSN fallback for a DriverContext", got)
	}

	// ctxDriver.Open returns an error naming this exact mistake, so a bridge
	// that took the fallback would fail here too rather than pass quietly.
	f := faultsql.New(&fault.Points{}, c)
	conn, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect through the bridge: %v", err)
	}
	_ = conn.Close()
}

// A DSN the driver rejects comes back from Connector, not from the first
// Connect.
//
// A sweep that discovered it on pass one would report a caller's mistake as
// the neighbour of an injected failure, and the two read alike.
func TestConnectorReturnsTheDriversOwnRefusal(t *testing.T) {
	t.Run("OpenConnector", func(t *testing.T) {
		base := &ctxDriver{openConnErr: errBase}
		if _, err := faultsql.Connector(base, "bad"); !errors.Is(err, errBase) {
			t.Errorf("Connector = %v, want the driver's own error unchanged", err)
		}
	})

	// The fallback cannot refuse a DSN, because driver.Driver has no method
	// that inspects one before Open. So the refusal arrives at Connect, and
	// unchanged.
	t.Run("Open", func(t *testing.T) {
		base := &openOnlyDriver{openErr: errBase}
		c, err := faultsql.Connector(base, "bad")
		if err != nil {
			t.Fatalf("Connector refused a DSN the driver has not seen yet: %v", err)
		}
		f := faultsql.New(&fault.Points{}, c)
		if _, err := f.Connect(context.Background()); !errors.Is(err, errBase) {
			t.Errorf("Connect = %v, want the driver's own error unchanged", err)
		}
	})
}

// Inserting the bridge does not shift the operation indexes.
//
// THIS IS STRUCTURAL, AND THE COMMENT SAYS SO RATHER THAN IMPLYING A CONTROL
// THAT CANNOT EXIST. Connector is a free function that receives no
// *fault.Points, so it cannot count even if someone wanted it to; no mutation
// of the bridge can make this test fail. An attempt at such a control was
// written and was a no-op, which is the shape this repository has already
// recorded twice today.
//
// What the test pins is the observable consequence: a caller who reaches the
// package through Connector sees the connect at index 1, exactly as a caller
// who builds a driver.Connector by hand does. That is the claim a reader cares
// about, and it would break if the bridge ever gained a Points.
func TestTheBridgeConsumesNoOperationIndex(t *testing.T) {
	reached := false
	for n, p := range fault.Sweep(t) {
		base := &openOnlyDriver{}
		c, err := faultsql.Connector(base, "file:spike.db")
		if err != nil {
			t.Fatalf("Connector: %v", err)
		}
		f := faultsql.New(p, c)

		conn, err := f.Connect(context.Background()) // 1
		if err != nil {
			if n == 1 {
				reached = true
				if !errors.Is(err, faultsql.ErrInjected) {
					t.Errorf("Connect = %v with operation 1 armed, want the injected error — the bridge took an index", err)
				}
			}
			continue
		}
		_ = conn.Close()
	}
	if !reached {
		t.Fatal("the sweep never armed operation 1, so nothing was asserted")
	}
}
