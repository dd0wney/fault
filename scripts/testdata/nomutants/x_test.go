package nomutants

import "testing"

func TestName(t *testing.T) {
	if Name == "" {
		t.Fatal("empty")
	}
}
