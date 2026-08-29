package fixture

import "errors"

var errBad = errors.New("bad")

// Load is a control coupling: it decides whether Save runs.
func Load(name string) error {
	if name == "" {
		return errBad
	}
	if len(name) > 100 {
		return errBad
	}
	return nil
}

// Save is a data coupling: it depends on bytes it does not own.
func (s *Store) Save(b []byte) error {
	if len(b) == 0 {
		return errBad
	}
	return nil
}

type Store struct{}
