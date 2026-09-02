package goroutine

import (
	"strings"
	"testing"
)

// parse is the whole reason this package can tell two goroutines apart, and
// every branch in it decides whether a goroutine is counted at all. Driving it
// through runtime.Stack reaches only the well-formed case.
func TestParseReadsAWellFormedBlock(t *testing.T) {
	block := "goroutine 42 [chan receive]:\n" +
		"main.worker()\n" +
		"\t/tmp/main.go:10 +0x1c\n" +
		"created by main.start in goroutine 1\n" +
		"\t/tmp/main.go:5 +0x40"

	id, desc, ok := parse(block)
	if !ok {
		t.Fatalf("parse rejected a well-formed block")
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
	if !strings.Contains(desc, "main.start") {
		t.Errorf("desc = %q, want it to name the creator", desc)
	}
	if !strings.Contains(desc, "goroutine 42") {
		t.Errorf("desc = %q, want it to carry the header", desc)
	}
}

// THE ID IS PARSED IN BASE TEN. An id of 19 read in base nine is 17, and two
// different goroutines can then collide on one key.
func TestParseReadsTheIDInBaseTen(t *testing.T) {
	id, _, ok := parse("goroutine 19 [running]:\nmain.f()")
	if !ok {
		t.Fatal("parse rejected the block")
	}
	if id != 19 {
		t.Errorf("id = %d, want 19 -- a base other than ten was used", id)
	}
}

// Identifiers exceed what a signed 63-bit value holds in a long-lived process.
func TestParseReadsAnIDAboveTheSignedRange(t *testing.T) {
	id, _, ok := parse("goroutine 9223372036854775808 [running]:\nmain.f()")
	if !ok {
		t.Fatal("parse rejected an id above the signed range")
	}
	if id != 9223372036854775808 {
		t.Errorf("id = %d, want 9223372036854775808", id)
	}
}

// A block with no "created by" line falls back to its first frame, which is
// still enough to find the goroutine.
func TestParseFallsBackToTheFirstFrame(t *testing.T) {
	_, desc, ok := parse("goroutine 7 [select]:\nruntime.gopark()\n\t/usr/go/proc.go:1 +0x2")
	if !ok {
		t.Fatal("parse rejected the block")
	}
	if !strings.Contains(desc, "runtime.gopark") {
		t.Errorf("desc = %q, want the first frame when there is no creator", desc)
	}
}

// A one-line block has no frame to fall back to, and must not index past it.
func TestParseSurvivesABlockWithNoFrames(t *testing.T) {
	id, desc, ok := parse("goroutine 8 [running]:")
	if !ok {
		t.Fatal("parse rejected a header-only block")
	}
	if id != 8 {
		t.Errorf("id = %d, want 8", id)
	}
	if strings.Contains(desc, "goroutine 8") == false {
		t.Errorf("desc = %q, want the header", desc)
	}
}

// Blocks that are not goroutines are refused rather than counted. The first
// block of a dump is often a header, and counting it would add a goroutine
// that does not exist.
func TestParseRefusesWhatIsNotAGoroutine(t *testing.T) {
	for _, block := range []string{
		"",
		"   ",
		"panic: something",
		"goroutine",
		"goroutine notanumber [running]:\nmain.f()",
		"goroutines 5 [running]:\nmain.f()",
	} {
		if _, _, ok := parse(block); ok {
			t.Errorf("parse accepted %q, which is not a goroutine block", block)
		}
	}
}

// The creator search takes the FIRST "created by" line. A later frame's
// creator belongs to a different goroutine in the same dump.
func TestParseTakesTheFirstCreator(t *testing.T) {
	_, desc, ok := parse("goroutine 3 [running]:\nmain.f()\ncreated by main.first\ncreated by main.second")
	if !ok {
		t.Fatal("parse rejected the block")
	}
	if !strings.Contains(desc, "main.first") {
		t.Errorf("desc = %q, want it to name the first creator, main.first", desc)
	}
	if strings.Contains(desc, "main.second") {
		t.Errorf("desc = %q, want the first creator only", desc)
	}
}

// grow doubles until the dump fits. A dump that exactly fills the buffer is
// TRUNCATED, so the loop must grow again rather than return it.
func TestGrowKeepsGoingUntilTheDumpFits(t *testing.T) {
	const need = (64 << 10) * 3

	calls := 0
	dump := func(buf []byte, _ bool) int {
		calls++
		if len(buf) < need {
			// Truncated: the runtime fills the buffer completely.
			for i := range buf {
				buf[i] = 'x'
			}
			return len(buf)
		}
		copy(buf, strings.Repeat("y", need))
		return need
	}

	got := grow(dump)
	if len(got) != need {
		t.Fatalf("grow returned %d bytes, want %d -- a truncated dump was accepted",
			len(got), need)
	}
	if strings.Contains(string(got), "x") {
		t.Errorf("grow returned truncated content")
	}
	if calls < 2 {
		t.Errorf("dump was called %d time(s), so the growth path never ran and "+
			"this test asserts nothing about it", calls)
	}
}

// A dump that fits at once is returned without growing.
func TestGrowReturnsTheFirstFitUntouched(t *testing.T) {
	calls := 0
	got := grow(func(buf []byte, _ bool) int {
		calls++
		copy(buf, "short")
		return len("short")
	})
	if string(got) != "short" {
		t.Errorf("grow = %q, want %q", got, "short")
	}
	if calls != 1 {
		t.Errorf("dump called %d times, want 1", calls)
	}
}

// A header with no state field is still a goroutine. The runtime prints
// "goroutine 42 [running]:", and a parser that DEMANDS the bracketed state
// silently drops any goroutine whose header it did not predict -- which is a
// count that is wrong in the safe-looking direction.
func TestParseAcceptsAHeaderWithNoState(t *testing.T) {
	id, _, ok := parse("goroutine 42")
	if !ok {
		t.Fatalf("parse refused a two-field header, so a goroutine would go uncounted")
	}
	if id != 42 {
		t.Errorf("id = %d, want 42", id)
	}
}

// A header with the prefix and NOTHING after it has no id to read. The guard
// must refuse it rather than index a field that is not there.
func TestParseRefusesAHeaderWithNoID(t *testing.T) {
	if _, _, ok := parse("goroutine "); ok {
		t.Error("parse accepted a header carrying no id")
	}
}

// A two-line block has exactly one frame, and that frame is the fallback.
// This is the smallest block where the fallback is reachable at all.
func TestParseFallsBackOnATwoLineBlock(t *testing.T) {
	_, desc, ok := parse("goroutine 9 [running]:\nmain.only()")
	if !ok {
		t.Fatal("parse rejected the block")
	}
	if !strings.Contains(desc, "main.only") {
		t.Errorf("desc = %q, want the single frame", desc)
	}
}
