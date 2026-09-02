package leak_test

import (
	"testing"

	"github.com/dd0wney/fault/alloc"
	"github.com/dd0wney/fault/fs"
	"github.com/dd0wney/fault/goroutine"
	"github.com/dd0wney/fault/leak"
	"github.com/dd0wney/fault/sql"
)

// The compile-time half: each adapter this package names must satisfy the
// interface leak reads it through. A dropped method breaks the build here,
// before any test runs.
var (
	_ leak.Counter = (*fs.Fault)(nil)
	_ leak.Counter = (*alloc.Fault)(nil)
	_ leak.Counter = (*sql.Fault)(nil)
	_ leak.Namer   = (*fs.Fault)(nil)
	_ leak.Delta   = goroutine.Snapshot{}
)

// TestTheAdaptersSatisfyTheInterfaces is the runtime half. A compile-time
// assertion cannot fail in a way "go test" reports -- it fails the build for
// the whole package, not one test -- so this boxes each concrete type as
// any and checks the same five assertions with the ok-form, which names the
// one that broke instead of only refusing to build.
func TestTheAdaptersSatisfyTheInterfaces(t *testing.T) {
	check := func(name string, ok bool) {
		if !ok {
			t.Errorf("%s no longer satisfies the interface leak needs from it", name)
		}
	}

	var fsFault any = (*fs.Fault)(nil)
	var allocFault any = (*alloc.Fault)(nil)
	var sqlFault any = (*sql.Fault)(nil)
	var snap any = goroutine.Snapshot{}

	_, ok := fsFault.(leak.Counter)
	check("*fs.Fault as leak.Counter", ok)
	_, ok = allocFault.(leak.Counter)
	check("*alloc.Fault as leak.Counter", ok)
	_, ok = sqlFault.(leak.Counter)
	check("*sql.Fault as leak.Counter", ok)
	_, ok = fsFault.(leak.Namer)
	check("*fs.Fault as leak.Namer", ok)
	_, ok = snap.(leak.Delta)
	check("goroutine.Snapshot as leak.Delta", ok)
}
