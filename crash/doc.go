// Package crash rebuilds a store's on-disk state as a crash would leave it,
// and runs a check on every state it can build.
//
// SQLite runs three loops: out-of-memory, I/O error, and crash.
// [github.com/dd0wney/fault/alloc] and [github.com/dd0wney/fault] are the first
// two. This is the third.
//
// The core's documentation names the gap in its own words: a sweep injects
// returned errors, not crashes. An error return is a cooperative failure -- the
// caller is told, and it unwinds, cleans up and reports -- and every defence a
// Go program has against a failed operation is cooperative. A crash runs none
// of them, and the state it leaves is whatever the storage layer had already
// made durable, which is not what the program had already written. So a store
// that writes straight to its destination file passes every sweep in this
// module, because after a failed write its own cleanup deletes the partial
// file and no bad state survives to be observed. The same store loses data on
// the first power cut between the write and the sync. The two loops find
// disjoint defect classes, and neither substitutes for the other.
//
// # Using it
//
//	rec := crash.Record(faultfs.OS(), dir)
//	if err := store.Save(rec, dir, "v2"); err != nil {
//		t.Fatal(err)
//	}
//
//	crash.Run(t, rec, crash.Model{Sector: 4096}, func(t *testing.T, fsys faultfs.FS) {
//		v, err := store.Open(fsys, dir)
//		if err != nil {
//			t.Fatalf("the store did not reopen: %v", err)
//		}
//		if v != "v1" && v != "v2" {
//			t.Errorf("the value is neither: %q", v)
//		}
//	})
//
// [Record] returns an [github.com/dd0wney/fault/fs.FS], so a scenario already
// written against the fs adapter needs no change. It wraps a real filesystem
// and serves every call for real, so the scenario writes real bytes to a real
// disk. [Run] then rebuilds each state a crash could have left, one subtest for
// each, and calls the check on it. The check is the loop body: what it asserts
// is the whole of what the sweep proves.
//
// # The durability model
//
// At the moment of the crash, which changes are on the disk? The model keeps
// two pending sets.
//
// File data. Every Write and every Truncate on an open file enters that file's
// pending set. A Sync on that file's handle clears it.
//
// Directory metadata. Every create, Rename, Remove and MkdirAll enters the
// pending set of the directory that holds the name. A Sync on a handle opened
// on that directory clears it -- a file sync does not reach it, which is the
// missing parent fsync this package exists to catch. A rename holds two names,
// so both directories must be covered.
//
// Close clears nothing. POSIX does not make close(2) flush, and a store that
// treats it as a flush holds the exact defect this package exists to find.
//
// An operation the filesystem refused is not recorded at all. It changed
// nothing, so a record that held it would describe a run that did not happen,
// and every state built from it reads exactly like a finding. The ordinary
// idiom `defer fsys.Remove(tmp)` after a successful rename makes that mistake
// reachable from correct code.
//
// The interface needs no new method for the directory rule. OpenFile(dir,
// os.O_RDONLY, 0) already returns a handle whose Sync the recorder sees, which
// is also how a Go program syncs a directory in production.
//
// A crash point sits after each operation that changes state. A read, a sync
// and a plain open change nothing, so none of them carries one.
//
// [Model.MetadataDurable] makes a create, rename, remove or mkdir durable as
// soon as it returns. The strict rule is the default on purpose: a false alarm
// costs a reader an hour, and a missed defect costs data.
//
// # Where a write is recorded
//
// A write is recorded at the handle's offset, and the recorder tracks that
// offset itself. [github.com/dd0wney/fault/fs.File] is Read, Write, Sync,
// Truncate and Close, with no Seek and no WriteAt, so the offset moves only by
// the bytes the handle's own reads and writes carry and addition is enough.
//
// A handle opened with os.O_APPEND starts at the size the file already holds,
// not at zero, because every write on it lands at the current end of the file.
// The recorder asks the base for that size, which is its own bookkeeping and
// takes no index, in the same way it asks whether the handle is a directory. A
// size the base will not give is a refusal rather than a guess.
//
// Two things are outside that rule, and both are refused rather than modelled.
// A base that offers Seek or WriteAt through a wider interface is one: the
// recorder never calls either, so a caller that type-asserts for them reaches
// past this package. Two handles appending to one file concurrently are the
// other: each tracks its own offset by addition, and the kernel decides the
// real one. The whole-record control is what catches either, because the replay
// stops matching the directory the scenario wrote.
//
// # Record once, replay many
//
// The scenario runs once. The record is then replayed, many times, into fresh
// directories. Up to crash point k the happy-path run is exactly what the live
// process did, because a process that has not yet died takes no other branch,
// and after k the dead process does nothing. One record therefore describes
// every crash point.
//
// The alternative is to re-run the scenario for each crash point and stop it
// there. That does not, on its own, produce power loss. The live directory
// still holds the unsynced bytes, because the operating system serves them to
// any reader on the same machine, so what a stopped re-run models is process
// death rather than power loss. The pending-write record is needed either way,
// so re-running adds runs without removing anything -- and it also needs panic
// and recover, a stability check, and the scenario inside a callback.
//
// # The positive control
//
// Before it builds any candidate state, [Run] replays the WHOLE record and
// compares the result with the directory the scenario actually wrote -- every
// name, every size, every byte.
//
// If that comparison fails, the replay is wrong, every state built after it is
// a fiction, and every finding is noise. [Run] fails the test and builds
// nothing. This is what recording once was chosen for: it converts the one
// weakness of an operation log into an assertion that runs on every scenario
// any user ever writes, rather than on the fixtures we happened to think of.
//
// # Naming a state
//
// An ordinal means nothing outside the run that produced it, and a subtest name
// has the harder duty of being matched later by go test -run. The names nest in
// two levels:
//
//	TestCrash/after=data.tmp:write2/lost=data.tmp:write3+cfg:rename1
//	TestCrash/after=data.tmp:write2/lost=none
//
// A unit is named structurally -- the file, the operation, and which occurrence
// of that operation on that file. A sector carries a suffix,
// data.tmp:write3.s2. A colon separates, because a slash would create a third
// level of nesting. When the lost set would make the name too long, the name
// becomes lost=6units:9f3a1c2b, a count and a stable hash of the structural
// keys, and the failure message names every unit in full.
//
// # Limitations
//
// Stated plainly, because the failure mode of a testing library is a false
// green.
//
// Nothing about a crash during recovery. The check reopens once. A store that
// damages its own state while repairing it, and then dies again, is out of
// reach of this package.
//
// Nothing about a filesystem whose reordering the model does not express. The
// model is an approximation with a name. A store that survives it may still
// fail on a device that behaves differently.
//
// Nothing about zero-fill. A byte range this package says was lost keeps its
// PRIOR CONTENT. It does not become zeroes. The prior content is already in the
// record, as an earlier entry, so keeping it is the truthful model of a write
// that never reached the disk.
//
// This is the sharpest modelling choice in the package, and it is made on
// purpose. A zeroed header can read as a structurally valid value -- an offset
// of 0, a length of 0, a version of 0 -- so a store parses it, follows it, and
// fails in a way it never fails in reality. That is a defect report the code
// under test cannot hold, and manufacturing one is worse than reporting
// nothing.
//
// The cost is that the zero-fill defect class is out of scope. A device that
// really does leave zeroes in an unwritten sector, and a store that mistakes
// those zeroes for data, are together a real failure that this package will
// never produce a state for.
//
// Nothing the check does not assert. A check that only opens the store proves
// that the store opens. This is the same trap the core documentation names for
// adapters: a method that calls Trip and ignores the answer keeps the pass
// count exactly right, so counting passes never proves anything.
//
// Nothing useful, under the POSIX metadata rule, when the filesystem under test
// cannot sync a directory. This is the sharpest of the four.
//
// The default model holds a rename pending until the directory that holds the
// name is synced. If the interface the store is written against offers no way
// to sync a directory, the store cannot make that rename durable by any means
// available to it, and the sweep then reports a defect that is unfixable
// through the seam: every state it complains about is one the code had no way
// to avoid reaching. A tool that reports an unfixable defect is worse than one
// that reports nothing, because a reader who cannot act on a finding learns to
// skip the next one.
//
// So an adapter whose filesystem cannot sync a directory must set
// [Model.MetadataDurable]. [github.com/dd0wney/fault/fs] can perform the sync
// on Linux and macOS, because OpenFile(dir, os.O_RDONLY, 0) returns a handle
// whose Sync reaches the directory. It cannot on Windows, and a store that must
// also run there is in exactly this position.
package crash
