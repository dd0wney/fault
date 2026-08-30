// A module whose ONLY external dependency is imported from a _test.go file.
//
// `go list -deps ./...` does not report a test import, so the gate read a
// dependency graph that excluded exactly the file where a third-party driver
// would arrive. This fixture is the control for that: it must exit 1.
//
// The replace directive points at a stub module inside this directory, so the
// fixture resolves with GOPROXY=off and CI needs no network.
module example.com/depfixture

go 1.23.0

require example.com/ext v0.0.0

replace example.com/ext => ./ext
