package sql_test

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	"github.com/dd0wney/fault"
	faultsql "github.com/dd0wney/fault/sql"
)

// mustSucceed fails the test on any error, and names the injected one, because
// with nothing armed an injected error is the invented failure this file
// exists to catch and any other error is the base refusing a legitimate call.
func mustSucceed(t *testing.T, op string, err error) {
	t.Helper()
	if errors.Is(err, faultsql.ErrInjected) {
		t.Fatalf("%s returned the injected error with nothing armed", op)
	}
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
}

// drain reads the current result set to its end and returns the row count.
// Any error is a failure of the property under test, because nothing is armed.
func drain(t *testing.T, rs *stdsql.Rows) int {
	t.Helper()
	n := 0
	for rs.Next() {
		var v int64
		if err := rs.Scan(&v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		n++
	}
	if err := rs.Err(); err != nil {
		if errors.Is(err, faultsql.ErrInjected) {
			t.Fatalf("Rows.Next returned the injected error with nothing armed")
		}
		t.Fatalf("Rows.Err: %v", err)
	}
	return n
}

// A zero Points arms nothing, so this adapter must fail nothing, on any of the
// nineteen methods the contract test lists as counted.
//
// TestAnUnarmedResultSetDeliversEveryRow proves it for rows.Next alone, which
// is the one method whose condition is compound and so the one the mutation
// gate could reach: `if r.armed && r.n == r.at` became `if true && ...` and
// nothing noticed. The other eighteen write `if c.f.trip()`, which has one
// operand, so no mutant expresses an invented failure there and a 1.00 score
// is not evidence about it. fs and alloc carry the same guard by hand for the
// same reason.
//
// An invented failure is worse than a missed one. A missed injection
// under-reports and its report stays true. An invented one reports a defect
// the code under test does not have, so every later finding needs a person to
// re-derive whether it is real.
//
// Two halves, because database/sql prefers the Context forms whenever the
// connection offers them, so the plain Prepare, Begin, Exec and Query on the
// wrapper are reachable only by calling the wrapper directly.
func TestAZeroPointsFailsNothing(t *testing.T) {
	t.Run("through database/sql", func(t *testing.T) {
		base := &testDriver{rows: 3, moreRows: 2}
		db, f := faultsql.OpenDB(&fault.Points{}, base)
		ctx := context.Background()

		mustSucceed(t, "Ping", db.PingContext(ctx))
		_, err := db.Exec("insert")
		mustSucceed(t, "Exec", err)
		_, err = db.ExecContext(ctx, "insert")
		mustSucceed(t, "ExecContext", err)

		rs, err := db.QueryContext(ctx, "select n")
		mustSucceed(t, "QueryContext", err)
		seen := drain(t, rs)
		if !rs.NextResultSet() {
			t.Fatalf("NextResultSet returned false with a second result set waiting: %v", rs.Err())
		}
		seen += drain(t, rs)
		mustSucceed(t, "Rows.Close", rs.Close())
		if seen != 5 {
			t.Errorf("the two result sets delivered %d rows, want 5", seen)
		}

		st, err := db.PrepareContext(ctx, "select n")
		mustSucceed(t, "PrepareContext", err)
		_, err = st.ExecContext(ctx)
		mustSucceed(t, "Stmt.ExecContext", err)
		srs, err := st.QueryContext(ctx)
		mustSucceed(t, "Stmt.QueryContext", err)
		if got := drain(t, srs); got != 2 {
			t.Errorf("the prepared query delivered %d rows, want 2", got)
		}
		mustSucceed(t, "Stmt.Rows.Close", srs.Close())
		mustSucceed(t, "Stmt.Close", st.Close())

		tx, err := db.BeginTx(ctx, nil)
		mustSucceed(t, "BeginTx", err)
		mustSucceed(t, "Commit", tx.Commit())
		tx, err = db.Begin()
		mustSucceed(t, "Begin", err)
		mustSucceed(t, "Rollback", tx.Rollback())

		mustSucceed(t, "DB.Close", db.Close())

		if err := f.Err(); err != nil {
			t.Errorf("Err() = %v, want nil: the sweep described nothing and refused nothing", err)
		}
		if got := f.MaxOutstanding(); got != 1 {
			t.Errorf("MaxOutstanding() = %d, want 1: the leak question was never live", got)
		}
		if got := f.Outstanding(); got != 0 {
			t.Errorf("Outstanding() = %d, want 0", got)
		}
		connects, closes := base.counts()
		if connects == 0 || connects != closes {
			t.Errorf("the base saw %d connects and %d closes, want equal and at least one", connects, closes)
		}
	})

	t.Run("the plain forms on the wrapper", func(t *testing.T) {
		f := faultsql.New(&fault.Points{}, &testDriver{rows: 2})

		c, err := f.Connect(context.Background())
		mustSucceed(t, "Connect", err)

		st, err := c.Prepare("select n")
		mustSucceed(t, "Prepare", err)
		_, err = st.Exec(nil) //nolint:staticcheck // driver.Stmt requires Exec, so the wrapper is tested through it
		mustSucceed(t, "Stmt.Exec", err)
		rs, err := st.Query(nil) //nolint:staticcheck // driver.Stmt requires Query, so the wrapper is tested through it
		mustSucceed(t, "Stmt.Query", err)
		rows := 0
		for {
			e := rs.Next(make([]driver.Value, 1))
			if errors.Is(e, io.EOF) {
				break
			}
			mustSucceed(t, "Rows.Next", e)
			rows++
		}
		if rows != 2 {
			t.Errorf("Stmt.Query delivered %d rows, want 2", rows)
		}
		mustSucceed(t, "Rows.Close", rs.Close())
		mustSucceed(t, "Stmt.Close", st.Close())

		tx, err := c.Begin() //nolint:staticcheck // driver.Conn requires Begin, so the wrapper is tested through it
		mustSucceed(t, "Begin", err)
		mustSucceed(t, "Commit", tx.Commit())
		tx, err = c.Begin() //nolint:staticcheck // driver.Conn requires Begin, so the wrapper is tested through it
		mustSucceed(t, "Begin again", err)
		mustSucceed(t, "Rollback", tx.Rollback())

		mustSucceed(t, "Conn.Close", c.Close())

		if got := f.Outstanding(); got != 0 {
			t.Errorf("Outstanding() = %d, want 0", got)
		}
	})
}
