// Package sql injects faults into a [database/sql] driver.
//
// It is the third adapter in this module, after
// [github.com/dd0wney/fault/fs] and [github.com/dd0wney/fault/alloc], and the
// first one written against an interface this module did not design.
//
// The design document is docs/design/2026-08-31-sql-adapter.md. Six decisions
// are recorded there with the measurement behind each. The two that shape
// everything in this file:
//
// A pool of more than one connection makes the operation index meaningless,
// because the N-th operation overall depends on which goroutine reached the
// driver first. [OpenDB] sets SetMaxOpenConns(1), and [Fault.Err] refuses when
// two connections are live at once anyway, because a caller can widen the pool
// afterwards and nothing else would say so.
//
// The injected error is deliberately not [database/sql/driver.ErrBadConn].
// database/sql retries a bad connection: sql.go:1579 runs its body twice and
// then calls fn(alwaysNewConn) once more, so one armed operation would become
// up to three driver calls and the count would stop meaning anything.
package sql

import (
	"context"
	stdsql "database/sql"
	"database/sql/driver"
	"errors"
	"sync"

	"github.com/dd0wney/fault"
)

// ErrInjected is the error every injected failure reports.
//
// It is not [driver.ErrBadConn] and it must never become one.
// TestTheInjectedErrorIsNotABadConnection asserts that rather than trusting
// this comment, because the consequence is silent: database/sql would swallow
// the error, retry on a fresh connection, and the operation the sweep armed
// would not be the operation the caller sees fail.
var ErrInjected = errors.New("fault/sql: injected failure")

// errConcurrentConns is held, not returned, for the same reason
// crash.Recorder holds its refusals: the scenario is written to handle
// database errors, and this is not one of those. It is this package saying it
// cannot describe what happened.
var errConcurrentConns = errors.New("fault/sql: two connections were live at once, so the operation index does not identify one operation — set SetMaxOpenConns(1), or use github.com/dd0wney/fault/role")

// Fault is a [driver.Connector] that fails one operation and counts the
// connections it hands out.
//
// New returns this concrete type rather than the interface, for the same
// reason [github.com/dd0wney/fault/fs.New] does: Outstanding and Err are not
// part of driver.Connector and a caller needs both.
type Fault struct {
	mu          sync.Mutex
	outstanding int   // connections handed out and not yet closed
	err         error // the first refusal; every later one is redundant

	p    *fault.Points
	base driver.Connector
}

// New returns a Connector that fails one operation, chosen by p.
//
// It wraps base rather than replacing it, so every operation the sweep does
// not fail reaches the real driver and the code under test takes the real
// path.
func New(p *fault.Points, base driver.Connector) *Fault {
	return &Fault{p: p, base: base}
}

// OpenDB returns a *sql.DB served by this connector, with SetMaxOpenConns(1)
// applied.
//
// The setting is applied here rather than left to the caller because the
// operation index is only stable under it. It is not sufficient on its own: a
// caller may widen the pool on the returned handle, and nothing in the
// database/sql API reports that back to this package. [Fault.Err] is the guard
// for that, and it measures the property rather than the setting.
//
// sql.OpenDB is used rather than sql.Register and sql.Open. Register installs a
// driver under a global name and panics on a duplicate, so a test that used it
// would have to invent a unique name per run and could never remove it.
func OpenDB(p *fault.Points, base driver.Connector) (*stdsql.DB, *Fault) {
	f := New(p, base)
	db := stdsql.OpenDB(f)
	db.SetMaxOpenConns(1)
	return db, f
}

// Outstanding reports how many connections have been opened and not closed.
//
// This is the leak half of what a fault-injection loop must assert, and it is
// the analogue of [github.com/dd0wney/fault/fs.Outstanding]. It counts what
// THIS adapter handed out, which is not what sql.DB.Stats().OpenConnections
// counts: that number includes connections the pool holds idle, and an idle
// pooled connection is not a leak. The difference between the two is itself
// worth reading, so the design keeps both rather than choosing one.
//
// The two rules are the same as the fs adapter's, and both are the opposite of
// the naive reading. A FAILED connect counts nothing, because it handed
// nothing out. A FAILED close still decrements, because the connection is gone
// either way.
func (f *Fault) Outstanding() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.outstanding
}

// Err reports the first refusal, or nil.
//
// A refusal is this package saying it cannot describe the run, so a caller
// must fail the test on it rather than treat it as a database error.
func (f *Fault) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

// fail records the first refusal. The caller holds f.mu.
func (f *Fault) fail(err error) {
	if f.err == nil {
		f.err = err
	}
}

// Driver satisfies [driver.Connector].
func (f *Fault) Driver() driver.Driver { return f.base.Driver() }

// Connect counts one operation and fails it when the sweep says so.
//
// A tripped connect returns no connection, so it increments nothing. Counting
// it would report a leak on every pass that fails a connect, which is most of
// them.
func (f *Fault) Connect(ctx context.Context) (driver.Conn, error) {
	f.mu.Lock()
	tripped := f.p.Trip()
	f.mu.Unlock()

	if tripped {
		return nil, ErrInjected
	}

	c, err := f.base.Connect(ctx)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	f.outstanding++
	// The refusal measures the property, not the setting. Two live connections
	// mean the operation index no longer identifies one operation, whatever
	// SetMaxOpenConns was told.
	if f.outstanding > 1 {
		f.fail(errConcurrentConns)
	}
	f.mu.Unlock()

	return &conn{f: f, base: c}, nil
}

// conn is one connection, whose operations continue the same count the
// connector started.
type conn struct {
	f    *Fault
	base driver.Conn
}

// Prepare counts one operation and fails it when the sweep says so.
func (c *conn) Prepare(query string) (driver.Stmt, error) {
	if c.f.trip() {
		return nil, ErrInjected
	}
	st, err := c.base.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &stmt{f: c.f, base: st}, nil
}

// Close counts one operation, and decrements whether or not it fails.
//
// A tripped Close still closes the real connection. This is the same exception
// the fs adapter makes, for the same reason: a Close that reports an error and
// leaves the resource open is not a failure any driver produces, and a sweep
// that modelled one would leak a connection on every pass.
func (c *conn) Close() error {
	tripped := c.f.trip()

	err := c.base.Close()

	c.f.mu.Lock()
	c.f.outstanding--
	c.f.mu.Unlock()

	if tripped {
		return ErrInjected
	}
	return err
}

// Begin counts one operation and fails it when the sweep says so.
//
// driver.Conn requires it, so it cannot wait for the transaction work. It
// trips rather than delegating silently: an operation that reaches the driver
// without being counted would shift every later index by one, and nothing in
// the output would say why.
func (c *conn) Begin() (driver.Tx, error) {
	if c.f.trip() {
		return nil, ErrInjected
	}
	// staticcheck SA1019 is correct that driver.Conn.Begin is deprecated, and
	// it is not actionable here. driver.Conn REQUIRES the method, so an
	// implementation must provide it and a wrapper must forward to the base's
	// version of it. ConnBeginTx is additional, not a replacement.
	base, err := c.base.Begin() //nolint:staticcheck // driver.Conn requires Begin; a wrapper must forward it
	if err != nil {
		return nil, err
	}
	return &tx{f: c.f, base: base}, nil
}

// PrepareContext is [driver.ConnPrepareContext].
//
// database/sql prefers it over Prepare when a Conn offers it. Both are one
// driver call, so implementing it changes no count -- but NOT implementing it
// would drop the caller's context, and a wrapper that silently removes
// cancellation is a wrapper that changes the program under test.
func (c *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if c.f.trip() {
		return nil, ErrInjected
	}
	p, ok := c.base.(driver.ConnPrepareContext)
	if !ok {
		// The base does not offer it, so neither can this wrapper honestly.
		// Falling back to Prepare here would drop ctx exactly as not
		// implementing it would, and it would hide that fact behind a method
		// that claims the capability.
		st, err := c.base.Prepare(query)
		if err != nil {
			return nil, err
		}
		return &stmt{f: c.f, base: st}, nil
	}
	st, err := p.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	return &stmt{f: c.f, base: st}, nil
}

// BeginTx is [driver.ConnBeginTx].
func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.f.trip() {
		return nil, ErrInjected
	}
	b, ok := c.base.(driver.ConnBeginTx)
	if !ok {
		// Same SA1019 reason as conn.Begin: the base offers only the deprecated
		// method, so forwarding to it is the only honest fallback.
		base, err := c.base.Begin() //nolint:staticcheck // the base offers only the deprecated Begin
		if err != nil {
			return nil, err
		}
		return &tx{f: c.f, base: base}, nil
	}
	base, err := b.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &tx{f: c.f, base: base}, nil
}

// Ping is [driver.Pinger]. It counts, because a caller asks for it.
//
// Without it, database/sql falls back to "is a connection available", so
// db.Ping would report success against a driver whose Ping fails. That is a
// wrapper hiding a failure the unwrapped driver reports.
func (c *conn) Ping(ctx context.Context) error {
	if c.f.trip() {
		return ErrInjected
	}
	p, ok := c.base.(driver.Pinger)
	if !ok {
		return nil
	}
	return p.Ping(ctx)
}

// ResetSession is [driver.SessionResetter], and it is the one forwarded
// operation that does NOT count.
//
// THIS IS A DELIBERATE EXCEPTION, and it is the opposite of the rule Begin
// follows. The pool calls ResetSession, not the caller, and it decides when
// from its own state: whether a connection was reused, how long it sat, what
// else the program did. Counting it would make the operation index depend on
// pool timing, so the N-th operation would be a different operation between
// two runs of the same scenario -- which is the exact property fork 2 exists
// to protect.
//
// It is forwarded rather than omitted. Omitting it would stop database/sql
// calling the base driver's reset at all, so the driver under test would keep
// state across pooled uses that it does not keep unwrapped.
func (c *conn) ResetSession(ctx context.Context) error {
	r, ok := c.base.(driver.SessionResetter)
	if !ok {
		return nil
	}
	return r.ResetSession(ctx)
}

// stmt is one prepared statement, whose operations continue the same count.
type stmt struct {
	f    *Fault
	base driver.Stmt
}

// NumInput does not count. It is a property of the statement, not an operation
// on the database: database/sql calls it to check the argument count before it
// performs anything, and a driver answers from memory.
func (s *stmt) NumInput() int { return s.base.NumInput() }

// Close counts, and closes the real statement whether or not it trips, for the
// same reason conn.Close does.
func (s *stmt) Close() error {
	tripped := s.f.trip()
	err := s.base.Close()
	if tripped {
		return ErrInjected
	}
	return err
}

func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	if s.f.trip() {
		return nil, ErrInjected
	}
	// SA1019: driver.Stmt REQUIRES Exec, so an implementation must provide it
	// and a wrapper must forward to the base's. StmtExecContext is additional.
	return s.base.Exec(args) //nolint:staticcheck // driver.Stmt requires Exec; a wrapper must forward it
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	if s.f.trip() {
		return nil, ErrInjected
	}
	// SA1019: the same as Exec above. driver.Stmt requires Query.
	return s.base.Query(args) //nolint:staticcheck // driver.Stmt requires Query; a wrapper must forward it
}

// tx is one transaction.
//
// Commit and Rollback both count. A caller that ignores the error from either
// is the defect class this package exists to find, and a failed Commit is the
// sharpest of them: the program believes the data is durable and it is not.
type tx struct {
	f    *Fault
	base driver.Tx
}

func (x *tx) Commit() error {
	if x.f.trip() {
		return ErrInjected
	}
	return x.base.Commit()
}

func (x *tx) Rollback() error {
	if x.f.trip() {
		return ErrInjected
	}
	return x.base.Rollback()
}

// trip counts one operation under the lock.
func (f *Fault) trip() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.p.Trip()
}
