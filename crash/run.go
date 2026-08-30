package crash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

var errNoMutations = errors.New("crash: the scenario changed nothing, so there is no crash point and the sweep proved nothing")

// state is one candidate directory, with the name that identifies it.
type state struct {
	point  int
	lost   []unit
	name   string
	result tree
}

// plan builds every state the model allows, and returns a diagnostic rather
// than failing a test.
//
// The split exists for the same reason the core splits walk out of Sweep: a
// test cannot observe t.Fatal on its own *testing.T, and testing.TB carries an
// unexported method, so nothing outside the testing package can supply a
// stand-in. Without the split, the four most important error paths here are
// unreachable from any test.
func plan(r *Recorder, m Model) ([]state, error) {
	// The held refusal comes first, before the control. A record the recorder
	// has already refused describes a run this package cannot rebuild, so the
	// control's verdict on it would be noise reported in place of the reason.
	if err := r.failure(); err != nil {
		return nil, err
	}
	if err := r.checkReplay(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	entries := append([]entry(nil), r.entries...)
	snap := r.snap
	r.mu.Unlock()

	points := crashPoints(entries)
	if len(points) == 0 {
		return nil, errNoMutations
	}

	byIndex := index(entries)
	seen := map[string]bool{}
	var out []state

	for _, k := range points {
		durable, pending := split(entries, k, m)
		us := units(entries, pending, k, m)

		lostSets, err := subsets(us, m.Cover)
		if err != nil {
			return nil, fmt.Errorf("at crash point %d (%s): %w", k, pointName(byIndex, k), err)
		}

		for _, lost := range lostSets {
			// Every entry after k is absent from both durable and pending, so
			// it never enters keep and replay never applies it: a crash at k
			// cannot be survived by work that had not happened yet.
			keep := make(map[int]bool, len(durable)+len(pending))
			partial := map[int][]unit{}
			for _, n := range durable {
				keep[n] = true
			}
			for _, n := range pending {
				keep[n] = true
			}
			// A whole unit leaves keep. A part of a write stays in keep and
			// sends its lost RANGES to partial instead, because the rest of
			// that write did land and replay must still apply it.
			for _, u := range lost {
				if u.whole() {
					keep[u.entry] = false
					continue
				}
				partial[u.entry] = append(partial[u.entry], u)
			}

			keep = closure(entries, keep)
			built, err := replay(snap, entries, keep, partial)
			if err != nil {
				return nil, fmt.Errorf("at crash point %d (%s), losing %s: %w",
					k, pointName(byIndex, k), stateName(byIndex, lost), err)
			}

			fp := fingerprint(built)
			if seen[fp] {
				continue
			}
			seen[fp] = true

			out = append(out, state{
				point:  k,
				lost:   lost,
				name:   stateName(byIndex, lost),
				result: built,
			})
		}
	}

	// A stable order, because Go randomises map iteration and -run must match
	// the same name on every run. Sorting here also makes the order independent
	// of how subsets happens to enumerate, so a change there cannot silently
	// move a name a developer has written down.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].point != out[j].point {
			return out[i].point < out[j].point
		}
		return out[i].name < out[j].name
	})
	return out, nil
}

// Run rebuilds every state the model allows and runs check on each, one
// subtest for each state.
//
// Run does not call t.Parallel. The states are independent, so the caller may
// call it inside check, and that is the caller's decision to make.
func Run(t *testing.T, rec *Recorder, m Model, check func(*testing.T, faultfs.FS)) {
	t.Helper()

	states, err := plan(rec, m)
	if err != nil {
		t.Fatal(err)
	}

	rec.mu.Lock()
	byIndex := index(rec.entries)
	root := rec.root
	rec.mu.Unlock()

	// plan sorts by crash point first, so the states of one point are already
	// adjacent and no map is needed to group them.
	for i := 0; i < len(states); {
		j := i
		for j < len(states) && states[j].point == states[i].point {
			j++
		}
		group := states[i:j]
		i = j

		t.Run(pointName(byIndex, group[0].point), func(t *testing.T) {
			for _, s := range group {
				t.Run(s.name, func(t *testing.T) {
					// Every failing subtest carries the full lost set, because
					// a long one is named by a count and a hash that say how
					// MANY units were lost but not which. Naming a unit costs
					// a sort of the whole record, so this runs on failure
					// only, and it runs unconditionally there rather than
					// guessing from the name which form it took.
					t.Cleanup(func() {
						if t.Failed() {
							t.Logf("lost: %s", lostDetail(byIndex, s.lost))
						}
					})

					dir := t.TempDir()
					if err := s.result.writeTo(dir); err != nil {
						t.Fatalf("cannot materialise the state: %v", err)
					}
					// The real OS filesystem, not the recorder's base: the
					// rebuilt directory is an ordinary directory on disk, and
					// a base that injects faults would fail the check for a
					// reason the crash state did not cause.
					check(t, &remapFS{base: faultfs.OS(), from: root, to: dir})
				})
			}
		})
	}
}

// lostDetail names every unit a state lost, in full and in a stable order.
func lostDetail(byIndex map[int]entry, lost []unit) string {
	if len(lost) == 0 {
		return "nothing"
	}
	return strings.Join(unitNames(byIndex, lost), " ")
}

// errOutsideRoot is what remapFS reports for a name it cannot serve.
var errOutsideRoot = errors.New("outside the recorded root, so no rebuilt state holds it")

// remapFS serves the rebuilt directory under the names the scenario used, so
// the check opens the store with the same path constants the run wrote with.
//
// A name outside from is REFUSED rather than served from the real filesystem.
// The most likely such name is the recorded root itself reached by another
// route, and that directory still holds the finished state the run wrote: a
// check that read it would find everything intact and report a pass for a
// crash state it never opened. The recorder refuses the same names for the
// same reason, and under is the one rule both use.
type remapFS struct {
	base     faultfs.FS
	from, to string
}

func (m *remapFS) path(op, name string) (string, error) {
	rel, ok := under(m.from, name)
	if !ok {
		return "", &os.PathError{Op: op, Path: name, Err: errOutsideRoot}
	}
	return filepath.Join(m.to, rel), nil
}

func (m *remapFS) OpenFile(name string, flag int, perm os.FileMode) (faultfs.File, error) {
	p, err := m.path("open", name)
	if err != nil {
		return nil, err
	}
	return m.base.OpenFile(p, flag, perm)
}

func (m *remapFS) Remove(name string) error {
	p, err := m.path("remove", name)
	if err != nil {
		return err
	}
	return m.base.Remove(p)
}

// Rename reports an *os.LinkError, not an *os.PathError, because it names two
// paths. The fs adapter makes the same distinction for the same reason: a
// store that type-switches on the error must see what the os package returns.
func (m *remapFS) Rename(oldname, newname string) error {
	from, okFrom := under(m.from, oldname)
	to, okTo := under(m.from, newname)
	if !okFrom || !okTo {
		return &os.LinkError{Op: "rename", Old: oldname, New: newname, Err: errOutsideRoot}
	}
	return m.base.Rename(filepath.Join(m.to, from), filepath.Join(m.to, to))
}

func (m *remapFS) Stat(name string) (os.FileInfo, error) {
	p, err := m.path("stat", name)
	if err != nil {
		return nil, err
	}
	return m.base.Stat(p)
}

func (m *remapFS) MkdirAll(name string, perm os.FileMode) error {
	p, err := m.path("mkdir", name)
	if err != nil {
		return err
	}
	return m.base.MkdirAll(p, perm)
}

// ReadDir reports Op "open", matching the fs adapter: the real implementation
// opens the directory first, so a failure surfaces as an open.
func (m *remapFS) ReadDir(name string) ([]os.DirEntry, error) {
	p, err := m.path("open", name)
	if err != nil {
		return nil, err
	}
	return m.base.ReadDir(p)
}

// under reports the path of name relative to base, and whether name lies inside
// base at all.
//
// Recorder.rel and remapFS.path must agree on that boundary. The recorder
// refuses a name it cannot rebuild, and the rebuilt filesystem refuses to serve
// one, so two copies of the rule would be free to drift apart and a drift would
// let a check reach the real directory the run wrote.
func under(base, name string) (string, bool) {
	p, err := filepath.Rel(base, name)
	if err != nil || p == ".." || strings.HasPrefix(p, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}
