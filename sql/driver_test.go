package sql_test

import (
	"context"
	"database/sql/driver"
	"io"
	"reflect"
	"sync"
)

// A driver.Driver written by hand, inside the module.
//
// The dependency gate forbids a real driver, and since 2026-08-31 it reads
// test imports too, so importing one from a _test.go file is caught rather
// than tolerated. That gate was closed deliberately before this package.
//
// It implements MORE of database/sql/driver than the adapter does, on purpose.
// A test driver that implemented exactly the wrapper's set would make
// TestTheWrapperForwardsWhatTheBaseImplements pass for free, which is the
// "testing a target that resembles the gate's target is not testing the gate"
// defect that scripts/mutation.sh already warns about in its own words.
type testDriver struct {
	mu sync.Mutex

	// connects counts how many times database/sql asked for a connection. It
	// is what proves an injected error did NOT send database/sql around its
	// bad-connection retry: that path would ask again.
	connects int
	// closes counts connections actually closed at the base.
	closes int

	// resetCount counts ResetSession calls that reached the base. The pool
	// calls ResetSession, so the adapter forwards it without counting it, and
	// a test needs to see that it arrived.
	resetCount int

	// prepareErr and beginErr, when set, are what the base returns instead of
	// a statement or a transaction. Without them the adapter's pass-through
	// paths are unreachable, and the mutation gate reported exactly that.
	prepareErr error
	beginErr   error

	// rows is how many rows a query returns. It exists so a test can prove
	// the operation count does NOT follow it.
	rows int

	// queryErr and execErr make the pass-through paths of QueryContext and
	// ExecContext reachable. stmtQueryErr does the same for stmt.Query, which
	// is a different path: it wraps the result set, so its error branch is its
	// own. Adding the field to testStmt was not enough on its own -- nothing
	// set it, and the mutation gate reported the branch still unreachable.
	queryErr     error
	execErr      error
	stmtQueryErr error

	// lastStmt is the statement most recently handed out, so a test can ask it
	// what context it saw.
	lastStmt *testStmt

	// connectErr, when set, is what the base returns instead of a connection.
	// A base that cannot fail leaves the adapter's own error path untested,
	// and the mutation gate reported exactly that: removing `return nil, err`
	// from Connect changed no test result.
	connectErr error
}

func (d *testDriver) Open(name string) (driver.Conn, error) {
	return d.Connect(context.Background())
}

func (d *testDriver) Driver() driver.Driver { return d }

func (d *testDriver) Connect(ctx context.Context) (driver.Conn, error) {
	d.mu.Lock()
	d.connects++
	err := d.connectErr
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &testConn{d: d}, nil
}

// stmtCtxErr reports what the most recently prepared statement saw as
// ctx.Err() when QueryContext ran on it.
func (d *testDriver) stmtCtxErr() error {
	d.mu.Lock()
	s := d.lastStmt
	d.mu.Unlock()
	if s == nil {
		return nil
	}
	return s.ctxErr()
}

func (d *testDriver) resets() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.resetCount
}

func (d *testDriver) counts() (connects, closes int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.connects, d.closes
}

// testConn implements driver.Conn plus five optional interfaces the adapter
// does not implement yet. Each one is a real member of the set database/sql
// type-asserts for at run time.
type testConn struct{ d *testDriver }

var (
	_ driver.Conn               = (*testConn)(nil)
	_ driver.ConnPrepareContext = (*testConn)(nil)
	_ driver.ConnBeginTx        = (*testConn)(nil)
	_ driver.QueryerContext     = (*testConn)(nil)
	_ driver.ExecerContext      = (*testConn)(nil)
	_ driver.Pinger             = (*testConn)(nil)
	_ driver.SessionResetter    = (*testConn)(nil)
)

func (c *testConn) Prepare(query string) (driver.Stmt, error) {
	c.d.mu.Lock()
	err := c.d.prepareErr
	c.d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	c.d.mu.Lock()
	qerr := c.d.stmtQueryErr
	c.d.mu.Unlock()
	st := &testStmt{queryErr: qerr}
	c.d.mu.Lock()
	c.d.lastStmt = st
	c.d.mu.Unlock()
	return st, nil
}

func (c *testConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	return c.Prepare(query)
}

func (c *testConn) Close() error {
	c.d.mu.Lock()
	c.d.closes++
	c.d.mu.Unlock()
	return nil
}

func (c *testConn) Begin() (driver.Tx, error) {
	c.d.mu.Lock()
	err := c.d.beginErr
	c.d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &testTx{}, nil
}

func (c *testConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c *testConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.d.mu.Lock()
	err := c.d.queryErr
	c.d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return c.rowsOf(), nil
}

// rowsOf builds a result set of the driver's configured length, defaulting to
// two rows so the tests written before `rows` existed keep their shape.
func (c *testConn) rowsOf() *testRows {
	n := c.d.rows
	if n == 0 {
		n = 2
	}
	out := make([][]driver.Value, n)
	for i := range out {
		out[i] = []driver.Value{int64(i)}
	}
	return &testRows{rows: out}
}

func (c *testConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.d.mu.Lock()
	err := c.d.execErr
	c.d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return testResult{}, nil
}

func (c *testConn) Ping(ctx context.Context) error { return nil }

func (c *testConn) ResetSession(ctx context.Context) error {
	c.d.mu.Lock()
	c.d.resetCount++
	c.d.mu.Unlock()
	return nil
}

type testStmt struct {
	queryErr error
	// lastCtxErr records ctx.Err() at the moment QueryContext ran. It is what
	// proves a caller's cancellation reached the base driver rather than being
	// dropped by the wrapper, which is exactly what ctxutil.go:81 does when
	// StmtQueryContext is missing.
	mu         sync.Mutex
	lastCtxErr error
}

func (s *testStmt) ctxErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCtxErr
}

var (
	_ driver.Stmt             = (*testStmt)(nil)
	_ driver.StmtExecContext  = (*testStmt)(nil)
	_ driver.StmtQueryContext = (*testStmt)(nil)
)

func (s *testStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return testResult{}, nil
}

func (s *testStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	s.mu.Lock()
	s.lastCtxErr = ctx.Err()
	s.mu.Unlock()
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	return &testRows{rows: [][]driver.Value{{int64(1)}, {int64(2)}}}, nil
}

func (s *testStmt) Close() error  { return nil }
func (s *testStmt) NumInput() int { return 0 }
func (s *testStmt) Exec(args []driver.Value) (driver.Result, error) {
	return testResult{}, nil
}
func (s *testStmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.queryErr != nil {
		return nil, s.queryErr
	}
	return &testRows{rows: [][]driver.Value{{int64(1)}, {int64(2)}}}, nil
}

type testResult struct{}

func (testResult) LastInsertId() (int64, error) { return 1, nil }
func (testResult) RowsAffected() (int64, error) { return 1, nil }

type testRows struct {
	rows [][]driver.Value
	n    int
}

func (r *testRows) Columns() []string { return []string{"n"} }
func (r *testRows) Close() error      { return nil }
func (r *testRows) Next(dest []driver.Value) error {
	if r.n >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.n])
	r.n++
	return nil
}

var (
	_ driver.Rows                           = (*testRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*testRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*testRows)(nil)
	_ driver.RowsColumnTypeNullable         = (*testRows)(nil)
	_ driver.RowsColumnTypeLength           = (*testRows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*testRows)(nil)
	_ driver.RowsNextResultSet              = (*testRows)(nil)
)

func (r *testRows) ColumnTypeScanType(int) reflect.Type               { return reflect.TypeOf(int64(0)) }
func (r *testRows) ColumnTypeDatabaseTypeName(int) string             { return "BIGINT" }
func (r *testRows) ColumnTypeNullable(int) (bool, bool)               { return false, true }
func (r *testRows) ColumnTypeLength(int) (int64, bool)                { return 0, false }
func (r *testRows) ColumnTypePrecisionScale(int) (int64, int64, bool) { return 0, 0, false }
func (r *testRows) HasNextResultSet() bool                            { return false }
func (r *testRows) NextResultSet() error                              { return io.EOF }

type testTx struct{}

func (testTx) Commit() error   { return nil }
func (testTx) Rollback() error { return nil }

// plainDriver implements the REQUIRED interfaces and nothing else.
//
// database/sql is written for exactly this driver as much as for a complete
// one, and the adapter's fallback branches are only reachable through it. The
// mutation gate found them unreachable: every `if !ok` path in PrepareContext,
// BeginTx, Ping and ResetSession survived, because the only test driver
// implemented all four optional interfaces.
type plainDriver struct {
	// prepareErr and beginErr make the FALLBACK error paths reachable. Without
	// them, PrepareContext and BeginTx can take their `if !ok` branch but never
	// see it fail, and the mutation gate reported exactly those two lines.
	prepareErr error
	beginErr   error
}

func (d *plainDriver) Open(name string) (driver.Conn, error) { return &plainConn{d: d}, nil }
func (d *plainDriver) Driver() driver.Driver                 { return d }
func (d *plainDriver) Connect(ctx context.Context) (driver.Conn, error) {
	return &plainConn{d: d}, nil
}

type plainConn struct{ d *plainDriver }

var _ driver.Conn = (*plainConn)(nil)

func (c *plainConn) Prepare(query string) (driver.Stmt, error) {
	if c.d.prepareErr != nil {
		return nil, c.d.prepareErr
	}
	// plainStmt, not testStmt: this driver's whole purpose is to implement the
	// required methods and nothing else, and a testStmt would carry the two
	// context interfaces with it.
	return plainStmt{}, nil
}

func (c *plainConn) Close() error { return nil }

func (c *plainConn) Begin() (driver.Tx, error) {
	if c.d.beginErr != nil {
		return nil, c.d.beginErr
	}
	return &testTx{}, nil
}

// plainStmt and plainRows implement the REQUIRED methods and nothing else.
//
// Without them the wrapper's `if !ok` branches at the statement and rows
// levels are unreachable, which is the shape the mutation gate reported twice
// already on this package: a fixture that resembles the wrapper's own set
// cannot test the wrapper.
type plainStmt struct{}

func (plainStmt) Close() error  { return nil }
func (plainStmt) NumInput() int { return 0 }
func (plainStmt) Exec([]driver.Value) (driver.Result, error) {
	return testResult{}, nil
}
func (plainStmt) Query([]driver.Value) (driver.Rows, error) {
	return &plainRows{n: 2}, nil
}

type plainRows struct{ i, n int }

func (r *plainRows) Columns() []string { return []string{"n"} }
func (r *plainRows) Close() error      { return nil }
func (r *plainRows) Next(dest []driver.Value) error {
	if r.i >= r.n {
		return io.EOF
	}
	dest[0] = int64(r.i)
	r.i++
	return nil
}
