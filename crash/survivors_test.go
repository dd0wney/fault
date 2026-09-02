package crash

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

// Each test in this file pins a survivor of the mutation gate's run at
// ee92b62, read one by one on 2026-09-02. The comment on each test names the
// change it detects, so the baseline row can be checked against this file. A
// survivor that no test names is recorded in the row as equivalent, with the
// reason.

// --- snapshot.go ---

// readTree on a root that does not exist must return an error and no tree.
// Without the WalkDir error return, the callback runs with a nil DirEntry and
// panics. Without the final return, it hands back an empty tree and nil, which
// Record takes as "the root is empty" and the run proceeds against a directory
// that is not there.
func TestReadTreeRefusesARootThatDoesNotExist(t *testing.T) {
	_, err := readTree(filepath.Join(t.TempDir(), "absent"))
	if err == nil {
		t.Fatal("readTree returned a tree for a root that does not exist")
	}
}

// The root itself is not a key. A "." entry would put a directory node into
// every fingerprint and every rebuilt state, and levels documents the same
// rule from the recorder's side.
func TestReadTreeHoldsNoKeyForTheRootItself(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readTree(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["."]; ok {
		t.Errorf("readTree keys = %q, want no entry for the root itself", got.keys())
	}
	if len(got) != 1 {
		t.Errorf("readTree holds %d entries, want 1", len(got))
	}
}

// A name whose bytes cannot be read must fail the snapshot, not become an
// empty file in it. A dangling symlink is the one such name that needs no
// permission change, so the case runs as root and in CI. Windows needs a
// privilege to create a symlink at all, so the test does not run there.
func TestReadTreeRefusesANameItCannotRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating a symlink needs a privilege on Windows, and the case needs one")
	}
	src := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "absent"), filepath.Join(src, "dangling")); err != nil {
		t.Fatal(err)
	}
	if _, err := readTree(src); err == nil {
		t.Fatal("readTree read a dangling symlink as an empty file")
	}
}

// writeTo must report a name it cannot place: a file where the tree wants a
// directory, and a directory where the tree wants a file. Without the
// returns, the state materialises with the name missing and the check runs
// against a directory that is not the state.
func TestWriteToRefusesANameAlreadyHeldByTheOtherKind(t *testing.T) {
	t.Run("a file where the tree wants a directory", func(t *testing.T) {
		dst := t.TempDir()
		if err := os.WriteFile(filepath.Join(dst, "d"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := (tree{"d": {dir: true}}).writeTo(dst); err == nil {
			t.Fatal("writeTo placed a directory over a file and reported nothing")
		}
	})
	t.Run("a directory where the tree wants a file", func(t *testing.T) {
		dst := t.TempDir()
		if err := os.Mkdir(filepath.Join(dst, "f"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := (tree{"f": {data: []byte("x")}}).writeTo(dst); err == nil {
			t.Fatal("writeTo wrote a file over a directory and reported nothing")
		}
	})
}

// --- state.go ---

// A walk of 63 units is refused with the same diagnostic as 64. Without the
// guard, 1<<63 is negative, the cap comparison passes, and the slice
// allocation panics on a negative capacity.
func TestSixtyThreeUnitsAreRefusedWithoutOverflow(t *testing.T) {
	u := make([]unit, 63)
	for i := range u {
		u[i] = unit{entry: i + 1}
	}
	_, err := subsets(u, Exhaustive)
	if !errors.Is(err, errTooManyStates) {
		t.Fatalf("subsets of 63 units returned %v, want %v", err, errTooManyStates)
	}
}

// The refusal names the exact state count, so a reader can size the walk they
// asked for. 13 units is the first count over the cap.
func TestTheCapRefusalNamesTheExactStateCount(t *testing.T) {
	u := make([]unit, 13)
	for i := range u {
		u[i] = unit{entry: i + 1}
	}
	_, err := subsets(u, Exhaustive)
	if err == nil {
		t.Fatal("13 units passed a cap of 4096 states")
	}
	for _, want := range []string{"13 pending units", "8192 states", "4096"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal = %q, want it to name %q", err, want)
		}
	}
}

// countOf switches to the 2^n form exactly where int64 stops holding the
// count. One below the switch is the largest exact count. At the switch a
// shift goes negative, and a form that showed the sign would state a size
// no walk has.
func TestCountOfSwitchesFormWhereInt64Overflows(t *testing.T) {
	cases := map[int]string{
		12: "4096",
		62: "4611686018427387904",
		63: "2^63",
		64: "2^64",
	}
	for n, want := range cases {
		if got := countOf(n); got != want {
			t.Errorf("countOf(%d) = %q, want %q", n, got, want)
		}
	}
}

// --- name.go ---

// safe must replace exactly the runes go test replaces. The oracle is t.Name
// itself: a subtest named with the rune shows what -run will match. The
// boundaries of testing.isSpace are the two ends of the U+2000..U+200A range
// and the five named runes above it, so those are the runes checked.
func TestSafeReplacesEveryRuneGoTestRewrites(t *testing.T) {
	for _, r := range []rune{'\t', 0x1680, 0x2000, 0x2005, 0x200a, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000} {
		// A distinct prefix for each rune, because every rewritten name would
		// otherwise be the same and go test would number the duplicates.
		p := fmt.Sprintf("r%04x", r) + string(r) + "b"
		t.Run(p, func(t *testing.T) {
			want := t.Name()[strings.LastIndex(t.Name(), "/")+1:]
			if got := safe(p); got != want {
				t.Errorf("safe(%q) = %q, and go test named this subtest %q", p, got, want)
			}
		})
	}

	// The neighbours outside the set stay as they are. go test escapes a
	// non-printable rune, which safe does not mirror, so these compare against
	// the rune itself and not against t.Name.
	for _, r := range []rune{0x1fff, 0x200b, 0x2027} {
		p := "a" + string(r) + "b"
		if got := safe(p); got != p {
			t.Errorf("safe(%q) = %q, want it unchanged", p, got)
		}
	}
}

// A unit whose entry is not in the index is named by its index, so a
// diagnostic about a corrupt record still says which entry it means.
func TestAUnitWithNoEntryIsNamedByItsIndex(t *testing.T) {
	if got := unitName(map[int]entry{}, unit{entry: 7}); got != "entry7" {
		t.Errorf("unitName = %q, want %q", got, "entry7")
	}
}

// The cap is 80 and inclusive: a name of exactly 80 characters stays
// readable, and one character more becomes the count and hash.
//
// The number is written here as a literal and not as maxNameLen, because a
// test that derives its fixture from the constant moves with the constant: a
// cap of 79 or 81 passed the first version of this test. 80 is the measured
// figure the comment on maxNameLen records.
func TestTheNameCapIsInclusive(t *testing.T) {
	// "lost=" plus "<path>:write1" for one unit: the path is sized so the full
	// name lands exactly on the cap, then one past it.
	at := 80 - len("lost=") - len(":write1")
	for _, c := range []struct {
		pathLen int
		hashed  bool
	}{{at, false}, {at + 1, true}} {
		entries := []entry{{n: 1, k: kWrite, path: strings.Repeat("p", c.pathLen)}}
		got := stateName(index(entries), []unit{{entry: 1}})
		full := len("lost=") + c.pathLen + len(":write1")
		if hashed := strings.Contains(got, "units:"); hashed != c.hashed {
			t.Errorf("a %d-character name gave %q, want hashed=%v", full, got, c.hashed)
		}
	}
}

// The hash part is 16 hex characters, 64 bits, which the comment on stateName
// sizes against a collision bound. A shorter one weakens that bound in
// silence, and a longer one changes every name a developer has written down.
func TestTheHashedNameCarriesSixteenHexCharacters(t *testing.T) {
	var entries []entry
	var lost []unit
	for i := 1; i <= 30; i++ {
		entries = append(entries, entry{n: i, k: kWrite, path: "averylongfilename"})
		lost = append(lost, unit{entry: i})
	}
	got := stateName(index(entries), lost)

	const prefix = "lost=30units:"
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("stateName = %q, want the prefix %q", got, prefix)
	}
	hash := got[len(prefix):]
	if len(hash) != 16 {
		t.Errorf("hash part %q has %d characters, want 16", hash, len(hash))
	}
	for _, c := range hash {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Errorf("hash part %q holds %q, which is not hex", hash, c)
		}
	}
}

// --- rebuild.go ---

// A truncate and a rename of a path no present entry created are refused, as
// a write and a remove are. Each would otherwise invent a file.
func TestReplayRefusesToTruncateOrRenameAFileNoPresentEntryCreated(t *testing.T) {
	cases := map[string]struct {
		entry entry
		verb  string
	}{
		"truncate": {entry: entry{n: 2, k: kTruncate, path: "a", size: 0, needs: []int{1}}, verb: "truncates"},
		"rename":   {entry: entry{n: 2, k: kRename, path: "a", to: "b", needs: []int{1}}, verb: "renames"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			entries := []entry{{n: 1, k: kCreate, path: "a"}, c.entry}
			// entry 1, the create, is absent -- only the entry that names it is
			// present.
			got, err := replay(tree{}, entries, map[int]bool{2: true}, nil)
			if err == nil || !strings.Contains(err.Error(), c.verb) || !strings.Contains(err.Error(), "no present entry created") {
				t.Fatalf("replay returned tree %v and error %v, want the %s no-present-entry-created refusal", got, err, c.verb)
			}
		})
	}
}

// presentRanges is the complement of the lost ranges, in order, with no range
// twice and none past the end. replay writes each range it returns, so a
// duplicate changes no bytes and only this test can see it. A range past the
// end is clamped rather than indexed, or the mark array is overrun.
func TestPresentRangesIsTheOrderedComplement(t *testing.T) {
	cases := []struct {
		name string
		size int64
		lost []unit
		want []unit
	}{
		{"nothing lost", 4, nil, []unit{{from: 0, to: 4}}},
		{"a middle range", 6, []unit{{from: 2, to: 4}}, []unit{{from: 0, to: 2}, {from: 4, to: 6}}},
		{"the tail", 4, []unit{{from: 3, to: 4}}, []unit{{from: 0, to: 3}}},
		{"everything", 3, []unit{{from: 0, to: 3}}, nil},
		{"a range past the end is clamped", 3, []unit{{from: 1, to: 9}}, []unit{{from: 0, to: 1}}},
		{"two lost bytes apart", 5, []unit{{from: 1, to: 2}, {from: 3, to: 4}}, []unit{{from: 0, to: 1}, {from: 2, to: 3}, {from: 4, to: 5}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := presentRanges(c.size, c.lost)
			if !slices.Equal(got, c.want) {
				t.Errorf("presentRanges(%d, %v) = %v, want %v", c.size, c.lost, got, c.want)
			}
		})
	}
}

// resize past the current end keeps the bytes it has and zero-fills the rest,
// which is what ftruncate does. Without the copy, a truncate that grows a file
// wipes it.
func TestResizeKeepsTheDataItGrows(t *testing.T) {
	got := resize([]byte("ab"), 4)
	if want := []byte("ab\x00\x00"); !bytes.Equal(got, want) {
		t.Errorf("resize = %q, want %q", got, want)
	}
}

// --- control.go ---

// The control must fail on a replay that fails. Without the return, the
// failed replay's nil tree is compared against the root, and a root that is
// empty matches it: a broken replay passes the control that exists to catch
// it.
func TestTheControlFailsOnAReplayThatFails(t *testing.T) {
	r := &Recorder{
		root: t.TempDir(),
		snap: tree{},
		// A write to a path that no entry created and the snapshot did not
		// hold, which replay refuses.
		entries: []entry{{n: 1, k: kWrite, path: "a", data: []byte("v")}},
	}
	err := r.checkReplay()
	if err == nil {
		t.Fatal("the control passed a record whose replay fails")
	}
	if !strings.Contains(err.Error(), "the replay of the whole record failed") {
		t.Errorf("the control said %q, want it to name the failed replay", err)
	}
}

// The control names a root it cannot read as that, and not as a directory
// that does not match. The two call for different repairs.
func TestTheControlNamesARootItCannotRead(t *testing.T) {
	r := &Recorder{
		root:    filepath.Join(t.TempDir(), "gone"),
		snap:    tree{},
		entries: []entry{{n: 1, k: kCreate, path: "a"}},
	}
	err := r.checkReplay()
	if err == nil {
		t.Fatal("the control passed a root it cannot read")
	}
	if !strings.Contains(err.Error(), "cannot read the recorded root") {
		t.Errorf("the control said %q, want it to name the unreadable root", err)
	}
}

// --- run.go ---

// plan must hand back a replay refusal rather than keep the tree the refused
// replay left. The closure makes the pairing unreachable from a real record,
// so the record is built by hand: a write whose needs omit its create. The
// root holds the file, so the whole-record control passes and the refusal
// comes from the walk.
func TestPlanRefusesAStateTheReplayRefuses(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a"), []byte("v"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &Recorder{
		root: root,
		snap: tree{},
		entries: []entry{
			{n: 1, k: kCreate, path: "a"},
			{n: 2, k: kWrite, path: "a", data: []byte("v"), needs: nil},
		},
	}
	states, err := plan(r, Model{})
	if err == nil {
		t.Fatalf("plan built %d states from a record whose subset replay fails", len(states))
	}
	if !strings.Contains(err.Error(), "no present entry created") {
		t.Errorf("plan said %q, want the replay's refusal", err)
	}
}

// A state that lost nothing says so in words. An empty string in the failure
// log would read as a missing line, and a one-unit loss must name the unit.
func TestLostDetailNamesAnEmptySetNothing(t *testing.T) {
	byIndex := index([]entry{{n: 1, k: kWrite, path: "a"}})
	if got := lostDetail(byIndex, nil); got != "nothing" {
		t.Errorf("lostDetail(nil) = %q, want %q", got, "nothing")
	}
	if got := lostDetail(byIndex, []unit{{entry: 1}}); got != "a:write1" {
		t.Errorf("lostDetail of one unit = %q, want %q", got, "a:write1")
	}
}

// Every method of the rebuilt filesystem refuses a name outside the root with
// errOutsideRoot, and the parent of the root is outside it.
//
// Without the returns, the base is called with an empty path. That fails
// too, but with the error of a missing file, so a check that read it would
// learn nothing about where its name went. Without the ".." case, the parent
// of the root is served from the parent of the rebuilt copy.
func TestTheRebuiltFilesystemRefusesEveryNameOutsideTheRoot(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()
	m := &remapFS{base: faultfs.OS(), from: from, to: to}

	outside := map[string]string{
		"the parent of the root": filepath.Dir(from),
		"a sibling of the root":  filepath.Join(filepath.Dir(from), "elsewhere"),
	}
	for label, name := range outside {
		t.Run(label, func(t *testing.T) {
			calls := map[string]func() error{
				"open":    func() error { _, err := m.OpenFile(name, os.O_RDONLY, 0); return err },
				"remove":  func() error { return m.Remove(name) },
				"stat":    func() error { _, err := m.Stat(name); return err },
				"mkdir":   func() error { return m.MkdirAll(name, 0o700) },
				"readdir": func() error { _, err := m.ReadDir(name); return err },
				"rename":  func() error { return m.Rename(name, filepath.Join(from, "in")) },
			}
			for op, call := range calls {
				if err := call(); !errors.Is(err, errOutsideRoot) {
					t.Errorf("%s(%q) = %v, want %v", op, name, err, errOutsideRoot)
				}
			}
		})
	}
}
