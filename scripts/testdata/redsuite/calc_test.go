package redsuite

import "testing"

// A suite that already fails. Every mutant would then count as killed and the
// score would read 1.000.
func TestAlreadyFailing(t *testing.T) { t.Fatal("this suite is broken on purpose") }
