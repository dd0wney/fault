package crash

import (
	"os"
	"path/filepath"
	"testing"

	faultfs "github.com/dd0wney/fault/fs"
)

func TestTheControlPassesForATruthfulRecord(t *testing.T) {
	dir := t.TempDir()
	r := Record(faultfs.OS(), dir)

	f, err := r.OpenFile(filepath.Join(dir, "tmp"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Rename(filepath.Join(dir, "tmp"), filepath.Join(dir, "data")); err != nil {
		t.Fatal(err)
	}

	if err := r.checkReplay(); err != nil {
		t.Errorf("the control failed on a truthful record: %v", err)
	}
}

// The control needs its own control. A watcher that cannot report the negative
// reports nothing, and this repository has already paid for that once.
func TestTheControlFailsWhenTheReplayIsWrong(t *testing.T) {
	dir := t.TempDir()
	r := Record(faultfs.OS(), dir)

	f, err := r.OpenFile(filepath.Join(dir, "a"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Damage the record the way a broken replay would: drop the last byte.
	// The control must notice, or it is decoration.
	for i := range r.entries {
		if r.entries[i].k == kWrite {
			r.entries[i].data = r.entries[i].data[:len(r.entries[i].data)-1]
		}
	}

	if err := r.checkReplay(); err == nil {
		t.Fatal("the control passed against a damaged record, so it can never fail")
	}
}
