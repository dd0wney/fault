package sql_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/dd0wney/fault"
	faultsql "github.com/dd0wney/fault/sql"
)

// Every exported method of every wrapper is either counted or deliberately
// not, and a method in neither is a defect this test names.
//
// Contract rule 3 says an operation that skips Trip is invisible to the sweep,
// and that nothing detects it except a test that counts independently and
// compares. Measured on fault/fs on 2026-08-31: a method added and implemented
// without Trip passed the whole suite, go vet, the coupling gate and the
// mutation gate at 1.000000. The gate cannot express that defect.
//
// This reflects over the CONCRETE wrapper types rather than the interfaces,
// because sql/sql.go implements sixteen of them and a method belongs to
// whichever one it belongs to. The concrete type is the thing a new method
// actually lands on.
//
// The key is Type.Method, not Method. Close is on three of these types and
// ExecContext on two, and a flat map would let one type's decision stand in
// for another's.
var sqlCounted = map[string]bool{
	"Fault.Connect": true,

	"conn.Prepare":        true,
	"conn.PrepareContext": true,
	"conn.Begin":          true,
	"conn.BeginTx":        true,
	"conn.Ping":           true,
	"conn.QueryContext":   true,
	"conn.ExecContext":    true,
	"conn.Close":          true,

	"stmt.Exec":         true,
	"stmt.Query":        true,
	"stmt.ExecContext":  true,
	"stmt.QueryContext": true,
	"stmt.Close":        true,

	"rows.Next":          true,
	"rows.NextResultSet": true,
	"rows.Close":         true,

	"tx.Commit":   true,
	"tx.Rollback": true,
}

// The reasons here must match sql/doc.go. If they disagree, one of them is
// wrong and the disagreement is the finding.
var sqlExempt = map[string]string{
	"conn.ResetSession": "the POOL calls it, and sql.go:1353 discards any error from it that is " +
		"not driver.ErrBadConn. ErrInjected is deliberately not one, so counting it would " +
		"consume an index, inject an error nobody sees, and report a pass.",

	"stmt.NumInput": "a property of the statement, not an operation on the database. " +
		"database/sql asks before it performs anything and a driver answers from memory.",

	"rows.Columns":                    "a property of the result set, like NumInput.",
	"rows.ColumnTypeScanType":         "a property of the result set.",
	"rows.ColumnTypeDatabaseTypeName": "a property of the result set.",
	"rows.ColumnTypeNullable":         "a property of the result set.",
	"rows.ColumnTypeLength":           "a property of the result set.",
	"rows.ColumnTypePrecisionScale":   "a property of the result set.",
	"rows.HasNextResultSet":           "a question about the result set. NextResultSet, which does the work, DOES count.",

	"Fault.Outstanding":    "this adapter's own API, not an operation on the driver.",
	"Fault.MaxOutstanding": "this adapter's own API, not an operation on the driver. It reads a high-water mark the counter already keeps, and performs nothing.",
	"Fault.Err":            "this adapter's own API, not an operation on the driver.",
	"Fault.Driver":         "returns the wrapped driver. It performs nothing.",
}

func TestEverySQLWrapperMethodIsCountedOrExempt(t *testing.T) {
	f := faultsql.New(&fault.Points{}, &testDriver{rows: 2})

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

	rs := queryRows(t, c)
	defer func() { _ = rs.Close() }()

	tx, err := c.Begin() //nolint:staticcheck // driver.Conn requires Begin, so the wrapper is reached through it
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	// The names are the ones the maps use, taken from the concrete types the
	// wrapper actually hands out rather than written down a second time.
	subjects := map[string]any{"Fault": f, "conn": c, "stmt": st, "rows": rs, "tx": tx}

	seen := map[string]bool{}
	for short, v := range subjects {
		typ := reflect.TypeOf(v)
		if typ.NumMethod() == 0 {
			t.Fatalf("reflection reported no exported methods on %s (%T), so this check compared nothing", short, v)
		}
		for i := range typ.NumMethod() {
			key := short + "." + typ.Method(i).Name
			seen[key] = true

			_, counted := sqlCounted[key]
			reason, exempt := sqlExempt[key]

			switch {
			case counted && exempt:
				t.Errorf("%s is listed as counted AND as exempt (%q). It is one or the other", key, reason)
			case !counted && !exempt:
				t.Errorf("%s exists on the wrapper and nothing here says whether it counts. "+
					"An operation that skips Trip is invisible to the sweep, and an exemption "+
					"nobody wrote down is indistinguishable from an oversight", key)
			}
		}
	}

	// A decision about a method that no longer exists is a decision about
	// nothing, and it hides the fact that the surface shrank.
	for key := range sqlCounted {
		if !seen[key] {
			t.Errorf("%s is listed as counted and no wrapper declares it", key)
		}
	}
	for key := range sqlExempt {
		if !seen[key] {
			t.Errorf("%s is listed as exempt and no wrapper declares it", key)
		}
	}
}
