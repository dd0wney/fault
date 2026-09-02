package leak_test

import (
	"github.com/dd0wney/fault/alloc"
	"github.com/dd0wney/fault/fs"
	"github.com/dd0wney/fault/goroutine"
	"github.com/dd0wney/fault/leak"
	"github.com/dd0wney/fault/sql"
)

// Each adapter this package names must satisfy the interface leak reads it
// through. The build is the check: a dropped method fails go vet and go
// test before any test runs, with the compiler's own message, which names
// the type and the missing method. A runtime echo of the same assertions
// could never run to report anything different -- it lives in this same
// package, so the build failure that a dropped method causes stops it from
// running at all, before it gets the chance to name the adapter that lost
// the method. fs.Fault's names-behind-the-count assertion is not here,
// because that interface is unexported; see namer_test.go, in package
// leak, for that one.
var (
	_ leak.Counter = (*fs.Fault)(nil)
	_ leak.Counter = (*alloc.Fault)(nil)
	_ leak.Counter = (*sql.Fault)(nil)
	_ leak.Delta   = goroutine.Snapshot{}
)
