package leak

import (
	"github.com/dd0wney/fault/fs"
)

// namer is unexported, so its compile-time assertion cannot sit beside the
// others in contract_test.go, which is package leak_test and so cannot name
// an unexported identifier of package leak. It sits here instead, in
// package leak, for the same reason contract_test.go gives for its own
// assertions: the build is the check. A dropped OpenPaths method fails go
// vet and go test here, before any test runs, with the compiler's own
// message naming *fs.Fault and the missing method.
var _ namer = (*fs.Fault)(nil)
