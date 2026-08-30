package sql_test

import (
	"context"
	"database/sql/driver"
	"errors"
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
			if err == nil {
				t.Error("the armed connect did not fail")
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
// database/sql type-asserts for optional interfaces at run time. sql.go:1777
// falls back to Prepare, then Query, then Close when a Conn implements neither
// QueryerContext nor Queryer, so a wrapper implementing fewer interfaces than
// the driver beneath it turns ONE driver call into THREE and moves every armed
// point after it.
//
// This reflects over both and fails on a difference. It is the direct lesson of
// PR #2: a method set tracked by hand in two places drifts, and the drift is
// invisible because everything still compiles.
//
// It records the KNOWN gaps rather than reporting a clean pass. Task 9 closes
// them. Writing them out means closing one is a visible edit here, and a NEW
// divergence is distinguishable from the six already scheduled.
func TestTheWrapperForwardsWhatTheBaseImplements(t *testing.T) {
	known := map[string]bool{
		"ConnPrepareContext": true,
		"ConnBeginTx":        true,
		"QueryerContext":     true,
		"ExecerContext":      true,
		"Pinger":             true,
		"SessionResetter":    true,
	}

	optional := map[string]reflect.Type{
		"ConnPrepareContext": reflect.TypeOf((*driver.ConnPrepareContext)(nil)).Elem(),
		"ConnBeginTx":        reflect.TypeOf((*driver.ConnBeginTx)(nil)).Elem(),
		"QueryerContext":     reflect.TypeOf((*driver.QueryerContext)(nil)).Elem(),
		"Queryer":            reflect.TypeOf((*driver.Queryer)(nil)).Elem(),
		"ExecerContext":      reflect.TypeOf((*driver.ExecerContext)(nil)).Elem(),
		"Execer":             reflect.TypeOf((*driver.Execer)(nil)).Elem(),
		"Pinger":             reflect.TypeOf((*driver.Pinger)(nil)).Elem(),
		"SessionResetter":    reflect.TypeOf((*driver.SessionResetter)(nil)).Elem(),
		"Validator":          reflect.TypeOf((*driver.Validator)(nil)).Elem(),
		"NamedValueChecker":  reflect.TypeOf((*driver.NamedValueChecker)(nil)).Elem(),
	}

	base := &testDriver{}
	f := faultsql.New(&fault.Points{}, base)

	wrapped, err := f.Connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = wrapped.Close() }()

	bare, err := base.Connect(context.Background())
	if err != nil {
		t.Fatalf("base connect: %v", err)
	}
	defer func() { _ = bare.Close() }()

	wt, bt := reflect.TypeOf(wrapped), reflect.TypeOf(bare)

	// The positive control. If the test driver implemented none of these, every
	// assertion below would pass while proving nothing at all.
	implementedByBase := 0
	for _, iface := range optional {
		if bt.Implements(iface) {
			implementedByBase++
		}
	}
	if implementedByBase < 6 {
		t.Fatalf("the test driver implements only %d optional interfaces, so this test cannot detect a wrapper that forwards none", implementedByBase)
	}

	for name, iface := range optional {
		baseHas, wrapperHas := bt.Implements(iface), wt.Implements(iface)
		switch {
		case baseHas && !wrapperHas && !known[name]:
			t.Errorf("the base implements driver.%s and the wrapper does not, so database/sql takes a different path through the wrapper and every armed point after it moves", name)
		case !baseHas && wrapperHas:
			t.Errorf("the wrapper implements driver.%s and the base does not, so it answers for a capability that is not there", name)
		case baseHas && wrapperHas && known[name]:
			t.Errorf("driver.%s is listed as a known gap and the wrapper now implements it — remove it from `known`", name)
		}
	}
}
