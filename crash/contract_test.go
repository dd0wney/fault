package crash_test

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/dd0wney/fault"
	"github.com/dd0wney/fault/crash"
	faultfs "github.com/dd0wney/fault/fs"
)

// Every method of the recorded interfaces takes an index, and this compares
// the list of methods driven against the interfaces themselves.
//
// THE RULE HERE IS NOT RULE 3, and the difference matters. An fs adapter
// counts operations so it can fail one. This package counts them so the index
// it records equals the index fault.Sweep would give the same operation, which
// is what a later version walking crash points through an injected error run
// depends on. So a READ takes an index here, where an fs adapter would have
// nothing to inject into one.
//
// The consequence is the same either way: a method that records nothing shifts
// every later index by one, and nothing in the output says why. Measured on
// fault/fs on 2026-08-31, a method that skipped its call passed the whole test
// suite, go vet, the coupling gate and the mutation gate at 1.000000.
//
// The optional methods are outside the interface comparison, because they are
// not members of fs.File and reflection over the interface cannot see them.
// TestTheRecorderOffersTheSameOptionalSetAsTheFaultAdapter reads them from a
// live handle instead, and TestEveryOptionalMethodTakesAnIndex drives each.
func TestEveryRecordedMethodTakesAnIndex(t *testing.T) {
	fsOps := map[string]func(*testing.T, *crash.Recorder, string){
		"OpenFile": func(t *testing.T, r *crash.Recorder, dir string) {
			f, err := r.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatalf("OpenFile: %v", err)
			}
			_ = f.Close()
		},
		"Stat": func(t *testing.T, r *crash.Recorder, dir string) {
			_, _ = r.Stat(filepath.Join(dir, "a"))
		},
		"ReadDir": func(t *testing.T, r *crash.Recorder, dir string) {
			_, _ = r.ReadDir(dir)
		},
		"MkdirAll": func(t *testing.T, r *crash.Recorder, dir string) {
			if err := r.MkdirAll(filepath.Join(dir, "d"), 0o750); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
		},
		"Rename": func(t *testing.T, r *crash.Recorder, dir string) {
			seed(t, r, dir, "from")
			if err := r.Rename(filepath.Join(dir, "from"), filepath.Join(dir, "to")); err != nil {
				t.Fatalf("Rename: %v", err)
			}
		},
		"Remove": func(t *testing.T, r *crash.Recorder, dir string) {
			seed(t, r, dir, "gone")
			if err := r.Remove(filepath.Join(dir, "gone")); err != nil {
				t.Fatalf("Remove: %v", err)
			}
		},
	}

	fileOps := map[string]func(*testing.T, faultfs.File){
		"Read":     func(t *testing.T, f faultfs.File) { _, _ = f.Read(make([]byte, 1)) },
		"Write":    func(t *testing.T, f faultfs.File) { _, _ = f.Write([]byte("x")) },
		"Sync":     func(t *testing.T, f faultfs.File) { _ = f.Sync() },
		"Truncate": func(t *testing.T, f faultfs.File) { _ = f.Truncate(0) },
		"Close":    func(t *testing.T, f faultfs.File) { _ = f.Close() },
	}

	coversInterface(t, "fs.FS", reflect.TypeOf((*faultfs.FS)(nil)).Elem(), keysOfFS(fsOps))
	coversInterface(t, "fs.File", reflect.TypeOf((*faultfs.File)(nil)).Elem(), keysOfFile(fileOps))

	// And each one actually records. A table entry that drives a method which
	// records nothing is a list that agrees with itself, which is the shape
	// this file exists to remove.
	for name, op := range fsOps {
		t.Run("FS/"+name, func(t *testing.T) {
			dir := t.TempDir()
			r := crash.Record(faultfs.OS(), dir)
			before := len(crash.Entries(r))
			op(t, r, dir)
			if got := len(crash.Entries(r)); got <= before {
				t.Errorf("%s recorded no entry (%d before, %d after), so every later index shifts by one", name, before, got)
			}
		})
	}
	for name, op := range fileOps {
		t.Run("File/"+name, func(t *testing.T) {
			dir := t.TempDir()
			r := crash.Record(faultfs.OS(), dir)
			f, err := r.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			// The handle must be released before t.TempDir cleans up. POSIX
			// unlinks an open file happily and Windows refuses, so without
			// this the subtests leak a handle and the cleanup fails there and
			// only there. Windows CI caught it; nothing local could.
			//
			// The Close subtest closes it as the operation under test, so this
			// is a second Close for that one. It returns an error, which is
			// ignored: the descriptor is already released either way, which is
			// the same POSIX guarantee the fs adapter's Close relies on.
			defer func() { _ = f.Close() }()

			before := len(crash.Entries(r))
			op(t, f)
			if got := len(crash.Entries(r)); got <= before {
				t.Errorf("%s recorded no entry (%d before, %d after), so every later index shifts by one", name, before, got)
			}
		})
	}
}

func coversInterface(t *testing.T, label string, iface reflect.Type, driven map[string]bool) {
	t.Helper()
	if iface.NumMethod() == 0 {
		t.Fatalf("reflection reported no methods on %s, so this check compared nothing", label)
	}
	for i := range iface.NumMethod() {
		name := iface.Method(i).Name
		if !driven[name] {
			t.Errorf("%s has %s and no table entry drives it, so no test proves it takes an index", label, name)
		}
	}
	for name := range driven {
		if _, ok := iface.MethodByName(name); !ok {
			t.Errorf("a table entry drives %s and %s does not declare it", name, label)
		}
	}
}

func keysOfFS(m map[string]func(*testing.T, *crash.Recorder, string)) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func keysOfFile(m map[string]func(*testing.T, faultfs.File)) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func seed(t *testing.T, r *crash.Recorder, dir, name string) {
	t.Helper()
	f, err := r.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("seed close %s: %v", name, err)
	}
}

// optionalMethodsOf reads the optional method set from a live handle: the
// methods it offers beyond fs.File. fs/positional_test.go computes the same
// set from the fault adapter's handle, and the two must agree, because a
// scenario written against the adapter and then recorded here performs the
// same calls on both.
func optionalMethodsOf(t *testing.T, f faultfs.File) []string {
	t.Helper()
	iface := reflect.TypeOf((*faultfs.File)(nil)).Elem()
	concrete := reflect.TypeOf(f)
	var out []string
	for i := range concrete.NumMethod() {
		name := concrete.Method(i).Name
		if _, isMember := iface.MethodByName(name); !isMember {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		t.Fatal("the handle offers no method beyond fs.File, so this check compares nothing")
	}
	return out
}

// The recorder and the fault adapter must offer the SAME optional set.
//
// This is the one authority for the optional methods, read from two live
// handles and compared. A method added to the adapter and not to the recorder
// is an operation a sweep can fail and a crash walk cannot see, and until
// 2026-09-02 each package carried a hand-written copy of the set and nothing
// compared them.
func TestTheRecorderOffersTheSameOptionalSetAsTheFaultAdapter(t *testing.T) {
	dir := t.TempDir()

	adapter := faultfs.New(&fault.Points{}, faultfs.OS())
	af, err := adapter.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = af.Close() }()

	rec := crash.Record(faultfs.OS(), dir)
	rf, err := rec.OpenFile(filepath.Join(dir, "r"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rf.Close() }()

	want, got := optionalMethodsOf(t, af), optionalMethodsOf(t, rf)
	if !slices.Equal(got, want) {
		t.Errorf("the recorder offers %v beyond fs.File and the fault adapter offers %v. "+
			"A method in one and not the other is an operation one loop sees and the other does not", got, want)
	}
}

// Every optional method the recorder offers takes an index, and the table
// that drives them is compared against the live set, so a new optional method
// with no table entry is caught here rather than found later as a shifted
// index nobody can explain.
func TestEveryOptionalMethodTakesAnIndex(t *testing.T) {
	optionalOps := map[string]func(*testing.T, faultfs.File){
		"Seek": func(t *testing.T, f faultfs.File) {
			s, ok := f.(seeker)
			if !ok {
				t.Fatal("no Seek")
			}
			_, _ = s.Seek(0, io.SeekStart)
		},
		"WriteAt": func(t *testing.T, f faultfs.File) {
			w, ok := f.(writerAt)
			if !ok {
				t.Fatal("no WriteAt")
			}
			_, _ = w.WriteAt([]byte("x"), 0)
		},
		"ReadAt": func(t *testing.T, f faultfs.File) {
			r, ok := f.(readerAt)
			if !ok {
				t.Fatal("no ReadAt")
			}
			_, _ = r.ReadAt(make([]byte, 1), 0)
		},
	}

	dir := t.TempDir()
	rec := crash.Record(faultfs.OS(), dir)
	probe, err := rec.OpenFile(filepath.Join(dir, "probe"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	live := optionalMethodsOf(t, probe)
	_ = probe.Close()

	var driven []string
	for name := range optionalOps {
		driven = append(driven, name)
	}
	sort.Strings(driven)
	if !slices.Equal(driven, live) {
		t.Errorf("the table drives %v and the recorder offers %v beyond fs.File", driven, live)
	}

	for name, op := range optionalOps {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			r := crash.Record(faultfs.OS(), dir)
			f, err := r.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer func() { _ = f.Close() }()

			before := len(crash.Entries(r))
			op(t, f)
			if got := len(crash.Entries(r)); got <= before {
				t.Errorf("%s recorded no entry (%d before, %d after), so every later index shifts by one", name, before, got)
			}
		})
	}
}
