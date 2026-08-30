package crash

import "fmt"

// checkReplay is this package's positive control, and Run calls it before it
// builds any candidate state.
//
// Replaying the WHOLE record must reproduce the directory the scenario
// actually wrote -- every name, every size, every byte. If it does not, the
// replay is wrong, every state built afterwards is a fiction, and every
// finding is noise. The operation log was chosen over a snapshot at each sync
// precisely because its one weakness converts into this assertion, which runs
// on every scenario a user ever writes rather than on the fixtures we thought
// of.
func (r *Recorder) checkReplay() error {
	r.mu.Lock()
	entries := append([]entry(nil), r.entries...)
	snap := r.snap
	root := r.root
	r.mu.Unlock()

	present := make(map[int]bool, len(entries))
	for _, e := range entries {
		present[e.n] = true
	}

	got, err := replay(snap, entries, present, nil)
	if err != nil {
		return fmt.Errorf("crash: the replay of the whole record failed, so no state it builds can be trusted: %w", err)
	}
	want, err := readTree(root)
	if err != nil {
		return fmt.Errorf("crash: cannot read the recorded root %q to check the replay: %w", root, err)
	}
	if d := diffTree(want, got); d != "" {
		return fmt.Errorf("crash: replaying the whole record did not reproduce the directory the scenario wrote, so the replay is wrong and every state is a fiction:\n%s", d)
	}
	return nil
}
