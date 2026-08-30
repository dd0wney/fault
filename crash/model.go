package crash

// Cover chooses which candidate states a sweep visits.
type Cover int

const (
	// Exhaustive visits every legal subset of the pending units. It carries a
	// cap, and passing the cap fails the test rather than visiting a subset,
	// because a partial walk reports a pass the same way a complete one does.
	//
	// It is first in the block on purpose, so it is the zero value.
	Exhaustive Cover = iota

	// Prefixes visits the states in which the pending units persist in log
	// order, so a state is a prefix of the pending sequence. It costs u+1
	// states rather than 2^u, and it is the deterministic escape from the cap.
	Prefixes
)

// Model says what a crash can lose.
//
// The zero Model is the strict one: a whole Write call is the unit, metadata
// obeys the POSIX rule, and the walk is exhaustive. A caller who has not
// thought about it gets the most demanding model rather than the emptiest one.
type Model struct {
	// Sector is the unit a pending write can lose, in bytes. Zero loses a
	// whole Write call.
	Sector int

	// SplitAtCrashOnly limits sector splitting to the write at the crash
	// point. Every earlier pending write is then lost whole.
	SplitAtCrashOnly bool

	// MetadataDurable makes a create, rename, remove or mkdir durable as soon
	// as it returns, instead of pending until the parent directory is synced.
	// Set it for a store that must also run on Windows, where a directory
	// handle cannot be synced.
	MetadataDurable bool

	// Cover chooses which of the states the model allows the walk actually
	// visits. Exhaustive, the zero value, visits every legal subset of the
	// pending units and refuses past its cap. Prefixes visits u+1 of them and
	// is the deterministic escape from that cap.
	Cover Cover
}
