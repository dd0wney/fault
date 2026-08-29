// Package alloc injects allocation failures into code under test.
//
// It is an adapter over [fault]. The core counts operations and decides which
// one must fail; this package knows what an allocation is, and what a refusal
// looks like.
//
// # What this can and cannot claim
//
// Go cannot substitute malloc. A Go program that exhausts memory is killed by
// the runtime or by the OOM killer -- there is no error to return and no stack
// to unwind. Any package claiming to test "out of memory" in Go claims
// something narrower than SQLite does, so this one says exactly what it covers.
//
//   - Covered: large, length-driven buffers, the ones sized from a record
//     header, a file section or a query result. Those have real failure paths,
//     because the size comes from data and the data can be wrong, hostile, or
//     simply larger than a budget allows.
//   - Not covered: every implicit allocation Go makes -- slice growth, map
//     rehashing, interface boxing, goroutine stacks. Nothing here touches them.
//
// That is a smaller claim than SQLite's, and it is the honest one. The value is
// the same in kind: an error path no other test can reach gets executed, and a
// memory budget becomes enforceable.
//
// # The two loops
//
// SQLite runs the allocator loop twice, and the second is not a repeat:
//
//	Rig the allocator to fail once on the N-th allocation for N=1,2,3,....
//	Rig the allocator to fail all allocations beginning with the N-th.
//
// The first finds handlers that cope with a single failure and continue. The
// second finds handlers that only work when a later allocation succeeds -- a
// cleanup path that allocates in order to clean up, which is the defect nobody
// writes a test for. [New] is the first loop. [NewAllFrom] is the second.
//
// # The adapter contract
//
// The rules every adapter follows live in the core package's documentation,
// under "Writing an adapter":
//
//	go doc github.com/dd0wney/fault
package alloc

import "errors"

// ErrNoMemory is returned when an allocation is refused. A caller should treat
// it as a failure to obtain a resource, never as corruption.
var ErrNoMemory = errors.New("alloc: allocation refused")

// Allocator supplies byte buffers.
//
// Free exists for accounting, not for memory management: Go's collector owns
// the memory either way. It is what lets a fault adapter report the number of
// buffers still outstanding, which is the second of the three assertions a
// fault-injection loop needs -- the operation failed, nothing leaked, the state
// is still valid. A caller with no notion of releasing a buffer may pass a
// no-op, and gives up leak detection by doing so.
//
// An implementation must be safe for concurrent use.
type Allocator interface {
	// Bytes returns a buffer of exactly n bytes, or an error.
	Bytes(n int) ([]byte, error)

	// Free records that a buffer is no longer in use.
	Free(b []byte)
}

// Go returns an Allocator backed by make. It is what ships, and it is what a
// sweep wraps.
func Go() Allocator { return goAlloc{} }

type goAlloc struct{}

func (goAlloc) Bytes(n int) ([]byte, error) { return make([]byte, n), nil }
func (goAlloc) Free([]byte)                 {}
