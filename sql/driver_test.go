package sql_test

import (
	"context"
	"database/sql/driver"
	"io"
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
	return &testStmt{}, nil
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
	return &testRows{rows: [][]driver.Value{{int64(1)}, {int64(2)}}}, nil
}

func (c *testConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return testResult{}, nil
}

func (c *testConn) Ping(ctx context.Context) error { return nil }

func (c *testConn) ResetSession(ctx context.Context) error {
	c.d.mu.Lock()
	c.d.resetCount++
	c.d.mu.Unlock()
	return nil
}

type testStmt struct{}

func (s *testStmt) Close() error  { return nil }
func (s *testStmt) NumInput() int { return 0 }
func (s *testStmt) Exec(args []driver.Value) (driver.Result, error) {
	return testResult{}, nil
}
func (s *testStmt) Query(args []driver.Value) (driver.Rows, error) {
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
type plainDriver struct{ pings int }

func (d *plainDriver) Open(name string) (driver.Conn, error) { return &plainConn{d: d}, nil }
func (d *plainDriver) Driver() driver.Driver                 { return d }
func (d *plainDriver) Connect(ctx context.Context) (driver.Conn, error) {
	return &plainConn{d: d}, nil
}

type plainConn struct{ d *plainDriver }

var _ driver.Conn = (*plainConn)(nil)

func (c *plainConn) Prepare(query string) (driver.Stmt, error) { return &testStmt{}, nil }
func (c *plainConn) Close() error                              { return nil }
func (c *plainConn) Begin() (driver.Tx, error)                 { return &testTx{}, nil }
