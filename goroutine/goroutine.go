// Package goroutine reports the goroutines a scenario started and did not stop.
//
// It answers the half of "nothing leaked" that a descriptor count cannot. A
// store constructor that fails after starting a background worker leaks that
// worker, and every file handle it took may still be released correctly, so
// [github.com/dd0wney/fault/fs.Fault.Outstanding] reports zero and is right.
// The two instruments are exactly as correct as each other and blind to
// different things.
//
// This was written from a measurement. Sweeping a peer project's store
// constructor through fault/fs found four error paths that leaked a file
// handle, and a separate reading of the same four paths found each of them
// also leaking two goroutines: an LSM flush worker and a compaction worker,
// started in a constructor and stopped only by a Close that the error path
// never reaches. The handle sweep could not see them and reported a clean
// result for that resource, correctly.
//
// # Using it
//
//	for n, p := range fault.Sweep(t) {
//		before := goroutine.Take()
//
//		store, err := Open(faultfs.New(p, faultfs.OS()), dir)
//		if err == nil {
//			_ = store.Close()
//		}
//
//		if extra := before.Since(2 * time.Second); len(extra) > 0 {
//			t.Errorf("op %d: %d goroutine(s) still running: %v", n, len(extra), extra)
//		}
//	}
//
// # Why a snapshot rather than a count
//
// A count is absolute, and goroutines leaked at one point of a sweep are still
// running at the next, so every later point inherits the blame. The first
// version of the harness this package came from reported 22 of 30 states
// leaking when the true figure was 4. A snapshot makes the reading a delta by
// construction: [Snapshot.Since] reports only goroutines that were not there
// when the snapshot was taken.
//
// # Why the settle
//
// A worker told to stop needs a moment to go. A check with no patience reports
// every clean shutdown as a leak, so Since waits for the set to empty and
// gives up only at the deadline. Pass zero when the answer is wanted now.
package goroutine

import (
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Snapshot is the set of goroutines running when [Take] was called.
//
// It holds identifiers rather than names. Two goroutines started by one
// function share a name, and a set keyed by name would report one where the
// truth is two.
type Snapshot struct {
	ids map[uint64]struct{}
}

// Take records the goroutines running now.
func Take() Snapshot {
	live := scan()
	ids := make(map[uint64]struct{}, len(live))
	for id := range live {
		ids[id] = struct{}{}
	}
	return Snapshot{ids: ids}
}

// Since returns the goroutines that started after s was taken and are still
// running, sorted, one entry for each goroutine.
//
// It waits up to settle for that set to empty, so a worker on its way out is
// not reported. Pass zero to read the current state without waiting.
//
// The caller's own goroutine was running when s was taken, so it is never
// reported. A Snapshot taken by a different goroutine than the one calling
// Since is still meaningful for the same reason: membership is decided by the
// snapshot, not by who asks.
func (s Snapshot) Since(settle time.Duration) []string {
	deadline := time.Now().Add(settle)
	for {
		extra := s.diff(scan())
		if len(extra) == 0 || !time.Now().Before(deadline) {
			return extra
		}
		time.Sleep(time.Millisecond)
	}
}

// diff returns the descriptions of live goroutines absent from the snapshot.
func (s Snapshot) diff(live map[uint64]string) []string {
	var extra []string
	for id, desc := range live {
		if _, had := s.ids[id]; !had {
			extra = append(extra, desc)
		}
	}
	sort.Strings(extra)
	return extra
}

// scan returns every running goroutine by identifier, with a description.
func scan() map[uint64]string {
	live := map[uint64]string{}
	for _, block := range strings.Split(string(stack()), "\n\n") {
		id, desc, ok := parse(block)
		if ok {
			live[id] = desc
		}
	}
	return live
}

// parse reads one block of [runtime.Stack] output.
//
// A block begins "goroutine 42 [chan receive]:" and its frames follow. The
// "created by" line names the function that started it, which is what a reader
// needs to find a leaked worker. A goroutine with no such line was started by
// the runtime, and its own first frame is used instead.
func parse(block string) (uint64, string, bool) {
	lines := strings.Split(strings.TrimSpace(block), "\n")
	head := lines[0]
	if !strings.HasPrefix(head, "goroutine ") {
		return 0, "", false
	}
	// The id is whatever follows the prefix, up to the next space. Reading it
	// this way rather than by splitting into fields removes a guard that could
	// never fire: TrimSpace has already run, so a head that still carries the
	// "goroutine " prefix necessarily has something after it. A length check
	// there was unreachable, and unreachable code cannot be tested, only
	// believed.
	idText, _, _ := strings.Cut(strings.TrimPrefix(head, "goroutine "), " ")
	id, err := strconv.ParseUint(idText, 10, 64)
	if err != nil {
		return 0, "", false
	}

	where := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "created by ") {
			where = strings.TrimPrefix(line, "created by ")
			break
		}
	}
	if where == "" && len(lines) > 1 {
		where = strings.TrimSpace(lines[1])
	}
	return id, head + " " + where, true
}

// stack returns the full [runtime.Stack] output, growing the buffer until it
// is not truncated.
//
// Truncation is the failure that matters here: a short buffer drops the
// goroutines at the end of the dump, and those are the most recently started
// ones, which is exactly the set this package reports.
func stack() []byte { return grow(runtime.Stack) }

// grow is stack's loop with the dump injected, so a test can drive the growth
// path. Without that seam the doubling is unreachable under test -- 64 KiB
// holds every dump a test produces -- and an unreachable loop is one that can
// be wrong in any way at all without a single assertion noticing.
func grow(dump func([]byte, bool) int) []byte {
	for size := 64 << 10; ; size *= 2 {
		buf := make([]byte, size)
		if n := dump(buf, true); n < size {
			return buf[:n]
		}
	}
}
