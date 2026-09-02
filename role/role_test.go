package role_test

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/dd0wney/fault/role"
)

// --- Key ---

func TestKeyStringIsPasteable(t *testing.T) {
	k := role.Key{Role: "flush", Op: "sync wal.log", Nth: 2}
	if got, want := k.String(), "flush/sync wal.log#2"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// --- Op counting ---

// Only the target role can be made to fail. Every other role is counted so that
// its operations shift no ordinal, and then permitted.
func TestOnlyTheTargetRoleFails(t *testing.T) {
	var tripped []string
	for n, p := range role.Sweep(t, "flush") {
		if p.Op("compact", "write sst") {
			tripped = append(tripped, "compact")
		}
		if p.Op("flush", "write wal") {
			tripped = append(tripped, "flush")
		}
		if p.Op("compact", "sync sst") {
			tripped = append(tripped, "compact")
		}
		_ = n
	}
	for _, r := range tripped {
		if r != "flush" {
			t.Errorf("role %q was made to fail, but the target is flush", r)
		}
	}
	if len(tripped) != 1 {
		t.Errorf("%d operations failed, want 1: flush performs one operation", len(tripped))
	}
}

// An unclassified operation gets its own counter. If it joined the target it
// would shift every later ordinal and the sweep would silently walk a different
// sequence; if it were dropped, the same thing happens.
//
// The check is that interleaving unclassified operations changes nothing a
// caller can observe: the same pass count, and the same operations failing.
func TestAnUnclassifiedOperationDoesNotShiftTheTarget(t *testing.T) {
	run := func(withUnknown bool) (passes int, failed []int) {
		err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
			passes++
			for op := 1; op <= 4; op++ {
				if withUnknown {
					p.Op("", "something nobody classified")
				}
				if p.Op("flush", "write wal") {
					failed = append(failed, op)
					break
				}
			}
			return true
		})
		if err != nil {
			t.Fatalf("withUnknown=%v: %v", withUnknown, err)
		}
		return passes, failed
	}

	plainPasses, plainFailed := run(false)
	mixedPasses, mixedFailed := run(true)

	if plainPasses != mixedPasses {
		t.Errorf("%d passes with unclassified operations, %d without: they shifted the walk",
			mixedPasses, plainPasses)
	}
	if !slices.Equal(plainFailed, mixedFailed) {
		t.Errorf("failed %v with unclassified operations, %v without", mixedFailed, plainFailed)
	}
}

// A role that performs no operations at all must be refused. The sweep would
// otherwise terminate on its first pass and report a pass, which reads exactly
// like a sweep that visited every error path and found nothing wrong.
func TestWalkRefusesARoleThatDoesNothing(t *testing.T) {
	passes := 0
	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		passes++
		p.Op("compact", "write sst") // a different role entirely
		return true
	})
	if !errors.Is(err, role.ErrNoOperations) {
		t.Errorf("err = %v, want ErrNoOperations", err)
	}
	if passes != 1 {
		t.Errorf("ran %d passes, want 1", passes)
	}
}

func TestWalkRefusesAWalkThatDoesNotTerminate(t *testing.T) {
	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		for range n {
			p.Op("flush", "write wal")
		}
		return true
	})
	if !errors.Is(err, role.ErrNoTermination) {
		t.Errorf("err = %v, want ErrNoTermination", err)
	}
}

// --- The model ---

func TestWalkMatchesTheModel(t *testing.T) {
	for k := 1; k <= 32; k++ {
		var passes int
		var failed []int

		err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
			passes++
			for op := 1; op <= k; op++ {
				// Another actor, interleaved, which must change nothing.
				p.Op("compact", "write sst")
				if p.Op("flush", "write wal") {
					failed = append(failed, op)
					break
				}
			}
			return true
		})
		if err != nil {
			t.Fatalf("k=%d: %v", k, err)
		}
		if passes != k+1 {
			t.Errorf("k=%d: %d passes, want %d", k, passes, k+1)
		}
		if len(failed) != k {
			t.Errorf("k=%d: %d operations failed, want %d", k, len(failed), k)
		}
	}
}

// --- Stability: the reason this package exists ---

// A role whose own sequence differs between runs makes "the N-th operation"
// name a different operation each time. The sweep terminates, passes, and
// proves nothing -- and it looks identical to one that works.
func TestWalkRefusesAnUnstableRole(t *testing.T) {
	pass := 0
	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		pass++
		// The sequence changes from the second pass onward, BEFORE the
		// injection point, which is the case that makes a sweep unsound.
		first, second := "write wal", "sync wal"
		if pass > 1 {
			first, second = "sync wal", "write wal"
		}
		p.Op("flush", first)
		p.Op("flush", second)
		p.Op("flush", "close wal")
		return true
	})

	if !errors.Is(err, role.ErrUnstable) {
		t.Fatalf("err = %v, want ErrUnstable", err)
	}
	// The message is the whole deliverable when this fires, so it is asserted
	// as carefully as the error identity. It must name the role, both
	// operations, and WHERE they diverged -- a reader with the names but not
	// the position still has to find it by hand.
	for _, want := range []string{"flush", `did "sync wal"`, `"write wal" in the previous pass`, "operation 1", "pass 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic must name %q, so a reader can act on it: %v", want, err)
		}
	}
}

// Divergence AFTER the injection point is legitimate: the fault changed what
// happened next. Comparing whole traces would fail every sweep at pass two.
func TestDivergenceAfterTheFaultIsNotInstability(t *testing.T) {
	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		for op := 1; op <= 4; op++ {
			if p.Op("flush", "write wal") {
				// The cleanup path, reached only on a failing pass, and
				// performing operations the successful passes never do.
				p.Op("flush", "remove tmp")
				p.Op("flush", "close wal")
				return true
			}
		}
		return true
	})
	if err != nil {
		t.Errorf("err = %v, want nil: a scenario may diverge after the fault", err)
	}
}

// The check must survive a role whose sequence is stable but whose length
// varies because a LATER pass reaches further.
func TestAGrowingPrefixIsNotInstability(t *testing.T) {
	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		for op := 1; op <= 6; op++ {
			if p.Op("flush", "write wal") {
				return true // stops early on a failing pass
			}
		}
		return true
	})
	if err != nil {
		t.Errorf("err = %v, want nil: a shorter failing pass is not instability", err)
	}
}

// --- Concurrency ---

// Several actors call Op at once, which is the situation this package exists
// for. Exactly one of the target's operations may trip. Run with -race.
func TestOpIsSafeForConcurrentUse(t *testing.T) {
	const actors = 32

	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		if n != actors/2 {
			// Every pass must still perform the work, or the walk sees a
			// scenario that did nothing.
			for range actors {
				p.Op("flush", "write wal")
			}
			return true
		}

		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			hits int
		)
		for i := range actors {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Half the goroutines are a different actor entirely, and
				// none of them may trip.
				if i%2 == 0 {
					if p.Op("compact", "write sst") {
						mu.Lock()
						hits += 100 // a wrong role tripped
						mu.Unlock()
					}
					return
				}
				if p.Op("flush", "write wal") {
					mu.Lock()
					hits++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()

		if hits != 1 {
			t.Errorf("%d operations tripped, want exactly 1", hits)
		}
		return true
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// A trace that SHORTENS before the injection point is instability, not an
// artefact of the fault. It is the case a naive min() bound hides: comparing
// fewer indices finds them equal and passes.
//
// If the fault fired on operation n then n operations were attempted, so the
// trace must hold at least n entries. Fewer means the role did less work for a
// reason unrelated to the fault.
//
// The graphdb implementation does not detect this. The argument is theirs and
// unrun there, so this test is the first evidence either way.
func TestWalkRefusesARoleWhoseTraceShortens(t *testing.T) {
	pass := 0
	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		pass++
		// Six operations, until the fourth pass, when the role does two.
		// Every prefix still agrees, so a min()-bounded comparison sees
		// nothing wrong.
		count := 6
		if pass >= 4 {
			count = 2
		}
		for range count {
			if p.Op("flush", "write wal") {
				return true
			}
		}
		return true
	})

	if !errors.Is(err, role.ErrUnstable) {
		t.Errorf("err = %v, want ErrUnstable: the role's trace shortened before "+
			"the injection point, which is instability and not an artefact", err)
	}
}

// An operation with no role becomes Unknown rather than staying nameless, so it
// gets a counter of its own like any other non-target role. Left as "", it would
// still be a distinct key from the target and would still not shift it -- but
// only by accident, and a later change to the comparison would break it
// silently.
func TestAnEmptyRoleBecomesUnknown(t *testing.T) {
	named, blank := runWithInterloper(t, role.Unknown), runWithInterloper(t, "")
	if named != blank {
		t.Errorf("an empty role produced %d passes and %q produced %d: they must behave alike",
			blank, role.Unknown, named)
	}
}

func runWithInterloper(t *testing.T, r role.Role) int {
	t.Helper()
	passes := 0
	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		passes++
		for range 3 {
			p.Op(r, "an operation nobody classified")
			if p.Op("flush", "write wal") {
				return true
			}
		}
		return true
	})
	if err != nil {
		t.Fatalf("role %q: %v", r, err)
	}
	return passes
}

// The shortening diagnostic must say how many operations it saw and how many it
// expected, or a reader cannot tell instability from a scenario they mis-wrote.
func TestTheShorteningDiagnosticNamesBothCounts(t *testing.T) {
	pass := 0
	err := role.Walk(t, "flush", func(n int, p *role.Points) bool {
		pass++
		count := 6
		if pass >= 4 {
			count = 2
		}
		for range count {
			if p.Op("flush", "write wal") {
				return true
			}
		}
		return true
	})
	if !errors.Is(err, role.ErrUnstable) {
		t.Fatalf("err = %v, want ErrUnstable", err)
	}
	for _, want := range []string{"performed 2 operations", "pass 4", "at least 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnostic must say %q: %v", want, err)
		}
	}
}
